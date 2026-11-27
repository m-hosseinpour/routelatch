#!/usr/bin/env python3
"""Post-fix R-28 off-link verification for 2d4da2a.

Reproduces the prior off-link topology (pool exit on 172.31.79.20 behind the
default gateway, default replaced by dummy0) and checks the fixed status
warning, its TTL, clear-on-success, and the unchanged fail-closed data path.

Evidence-only helper: no product code is touched. The only additions are
samples under the evidence directory.
"""
import argparse
import datetime
import json
import os
import shlex
import subprocess
import time

ARGS = None
RESULTS = []


def run(cmd, timeout=120):
    return subprocess.run(cmd, capture_output=True, text=True, timeout=timeout)


def ax(script, user=None, timeout=120):
    cmd = ["docker", "exec"]
    if user:
        cmd += ["-u", user, "-e", "HOME=/home/rl"]
    cmd += [ARGS.a, "sh", "-c", script]
    return run(cmd, timeout=timeout)


def axdetach(script):
    return run(["docker", "exec", "-d", "-u", "1500:1500", "-e", "HOME=/home/rl",
                ARGS.a, "sh", "-c", script])


def rx(script, timeout=120):
    return run(["docker", "exec", ARGS.r, "sh", "-c", script], timeout=timeout)


def rxdetach(script):
    return run(["docker", "exec", "-d", ARGS.r, "sh", "-c", script])


def cli(args, timeout=90):
    return run(["docker", "exec", "-u", "1500:1500", "-e", "HOME=/home/rl",
                ARGS.a, ARGS.binary, "--json", "--data-dir", ARGS.data] + list(args),
               timeout=timeout)


def clitext(args, timeout=90):
    return run(["docker", "exec", "-u", "1500:1500", "-e", "HOME=/home/rl",
                ARGS.a, ARGS.binary, "--data-dir", ARGS.data] + list(args),
               timeout=timeout)


def env_of(result):
    for line in reversed((result.stdout or "").strip().splitlines()):
        if line.strip().startswith("{"):
            try:
                return json.loads(line)
            except json.JSONDecodeError:
                continue
    return None


def tun_obj():
    r = cli(["tun", "status"])
    data = (env_of(r) or {}).get("data") or {}
    if isinstance(data, dict) and "tun" in data:
        data = data["tun"]
    return data or {}, r.returncode


def status_env():
    r = cli(["status"], timeout=30)
    data = (env_of(r) or {}).get("data") or {}
    return data, r


def pool_obj():
    r = cli(["pool", "list"])
    return (env_of(r) or {}).get("data") or {}


def fields(obj):
    return {k: obj.get(k) for k in
            ("state", "desired", "helper", "interface", "last_error", "detail")}


def wait_for(fn, timeout, interval=0.05):
    end = time.time() + timeout
    while time.time() < end:
        try:
            if fn():
                return True
        except Exception:
            pass
        time.sleep(interval)
    return False


def link_present():
    return ax("ip link show rl-tun0 >/dev/null 2>&1").returncode == 0


def ev(name, text):
    with open(os.path.join(ARGS.evidence, name), "a", encoding="utf-8") as f:
        f.write(text.rstrip("\n") + "\n")


def check(name, ok, detail=""):
    RESULTS.append(("PASS" if ok else "FAIL", name, detail))
    ev("results.txt", f"{'PASS' if ok else 'FAIL'} {name}" + (f" :: {detail}" if detail else ""))


def notrun(name, reason):
    RESULTS.append(("NOTRUN", name, reason))
    ev("results.txt", f"NOTRUN {name} :: {reason}")


def warnings(st):
    return list(st.get("warnings") or [])


def has_egress_warning(st):
    return any(EGRESS_WARNING in str(w) for w in warnings(st))


EGRESS_WARNING = ("recent approved egress dial failed; "
                  "traffic remains fail-closed with no direct fallback")


def sample(tag):
    t, t_rc = tun_obj()
    st, st_result = status_env()
    tun_text = clitext(["tun", "status"])
    status_text = clitext(["status"])
    pool = pool_obj()
    rec = {
        "tag": tag,
        "t": time.time(),
        "tun": fields(t),
        "tun_rc": t_rc,
        "status_ready": st.get("ready"),
        "warnings": warnings(st),
        "egress_warning": has_egress_warning(st),
        "status_tun": fields(st.get("tun") or {}),
        "status_rc": st_result.returncode,
        "tun_text": (tun_text.stdout or "").strip(),
        "tun_text_rc": tun_text.returncode,
        "status_text": (status_text.stdout or "").strip(),
        "status_text_rc": status_text.returncode,
        "status_text_stderr": (status_text.stderr or "").strip(),
        "pool_counts": pool.get("counts"),
        "pool_records": [
            {"id": r.get("id"), "status": r.get("status"),
             "health": r.get("health"), "last_failure_kind": r.get("last_failure_kind")}
            for r in (pool.get("records") or [])
        ],
    }
    with open(os.path.join(ARGS.evidence, f"status-{tag}.json"), "w",
              encoding="utf-8") as f:
        json.dump({"status": st, "tun_status_text": rec["tun_text"],
                   "status_text": rec["status_text"], "pool": pool},
                  f, indent=2, sort_keys=True)
        f.write("\n")
    ev("samples.jsonl", json.dumps(rec, sort_keys=True))
    ev("steps.txt", f"\n[{tag}]\n" + json.dumps(rec, indent=2, sort_keys=True))
    return rec


def probe(host, timeout=6):
    script = (f"python3 /src/spike/tun/h/app_source.py --host {shlex.quote(host)} "
              f"--port 7110 --timeout {timeout}")
    r = ax(script, user="1500:1500", timeout=timeout + 10)
    try:
        return json.loads(r.stdout.strip().splitlines()[-1])
    except Exception:
        return {"ok": False, "error": (r.stdout or "") + (r.stderr or ""), "ms": 0}


def peer_of(p):
    if not p.get("ok"):
        return None
    try:
        return json.loads(p["raw"]).get("peer")
    except Exception:
        return None


def approved_direct(events):
    """Accepts for the approved destination whose peer is the client's own
    physical address, i.e. a direct fallback rather than the exit."""
    return [x for x in events if x.get("event") == "accept"
            and x.get("local") == "203.0.113.7" and x.get("peer") == "172.31.78.10"]


def log_json(path):
    r = rx(f"cat {path} 2>/dev/null || true")
    out = []
    for line in (r.stdout or "").splitlines():
        try:
            out.append(json.loads(line))
        except json.JSONDecodeError:
            pass
    return out


def main():
    global ARGS
    p = argparse.ArgumentParser()
    p.add_argument("--a", required=True)
    p.add_argument("--r", required=True)
    p.add_argument("--binary", required=True)
    p.add_argument("--data", required=True)
    p.add_argument("--evidence", required=True)
    p.add_argument("--gw", default="172.31.78.21")
    p.add_argument("--exit-iface", default="eth1")
    ARGS = p.parse_args()
    os.makedirs(ARGS.evidence, exist_ok=True)
    with open(os.path.join(ARGS.evidence, "results.txt"), "w", encoding="utf-8") as f:
        f.write("# post-fix R-28 off-link verification (2d4da2a)\n")
    for name in ("steps.txt", "samples.jsonl"):
        open(os.path.join(ARGS.evidence, name), "w").close()

    # ------------------------------------------------------------ peer fixture
    rx("pkill -f '^python3 /src/spike/tun/h/socks_exit.py' 2>/dev/null || true; "
       "pkill -f '^python3 /src/spike/tun/h/source_server.py' 2>/dev/null || true; "
       "sleep 0.3; rm -f /work/logs/*.log /work/logs/*.out; "
       "ip addr add 172.31.79.20/32 dev " + ARGS.exit_iface + " 2>/dev/null || true; "
       "ip addr add 203.0.113.7/32 dev eth0 2>/dev/null || true; "
       "ip addr add 203.0.113.8/32 dev eth0 2>/dev/null || true; "
       "sysctl -q -w net.ipv4.conf.eth0.arp_ignore=1; "
       "sysctl -q -w net.ipv4.conf.eth0.arp_filter=1; "
       "sysctl -q -w net.ipv4.ip_forward=0")
    rxdetach("exec python3 /src/spike/tun/h/socks_exit.py --listen 172.31.79.20 "
             "--port 11080 --log /work/logs/exit.log >/work/logs/exit.out 2>&1")
    rxdetach("exec python3 /src/spike/tun/h/source_server.py "
             "--log /work/logs/source.log >/work/logs/source.out 2>&1")
    ok = wait_for(lambda: rx("ss -lnt | grep -q ':11080 ' && ss -lnt | grep -q ':7110 '").returncode == 0, 10)
    check("off-link peer exit and source fixture listening", ok,
          rx("ss -lnt | grep -E ':(11080|7110) ' || true").stdout.strip().replace("\n", "; "))

    # ------------------------------------------------------------ engine setup
    r = ax(f"SUDO_UID=1500 SUDO_GID=1500 {shlex.quote(ARGS.binary)} --json tun setup --socks 127.0.0.1:8318")
    ev("steps.txt", "\n$ tun setup\n" + (r.stdout or "") + (r.stderr or "") + f"[exit {r.returncode}]")
    check("tun setup exit 0", r.returncode == 0, f"exit={r.returncode}")

    ax("rm -f /home/rl/engine-fix.out")
    axdetach(f"exec {shlex.quote(ARGS.binary)} --data-dir {shlex.quote(ARGS.data)} "
             "run --headless >>/home/rl/engine-fix.out 2>&1")
    ready = wait_for(lambda: status_env()[0].get("ready") is True, 30)
    check("engine ready", ready, json.dumps(warnings(status_env()[0])))

    r = cli(["import", "link", "socks5://172.31.79.20:11080#exit-off"])
    ev("steps.txt", "\n$ import link socks5://172.31.79.20:11080#exit-off\n"
       + (r.stdout or "") + (r.stderr or "") + f"[exit {r.returncode}]")
    check("import off-link pool exit", r.returncode == 0, f"exit={r.returncode}")
    r = cli(["rules", "add", "--kind", "ip", "203.0.113.7"])
    ev("steps.txt", "\n$ rules add --kind ip 203.0.113.7\n"
       + (r.stdout or "") + (r.stderr or "") + f"[exit {r.returncode}]")
    check("approved IP rule", r.returncode == 0, f"exit={r.returncode}")

    r = cli(["tun", "on"])
    ev("steps.txt", "\n$ tun on\n" + (r.stdout or "") + (r.stderr or "") + f"[exit {r.returncode}]")
    on = wait_for(lambda: tun_obj()[0].get("state") == "on", 30)
    check("tun on and link up", r.returncode == 0 and on, json.dumps(tun_obj()[0]))

    # ------------------------------------------------------------ off-link proof
    routes = ax("ip route; echo ---get-oif-eth0; ip route get 172.31.79.20 oif eth0 2>&1").stdout
    ev("topology.txt", "$ routes before change\n" + routes)
    check("pool exit is off-link (via the gateway on eth0, no connected /24)",
          f"172.31.79.20 via {ARGS.gw} dev eth0" in routes
          and "172.31.79.0/24" not in routes,
          routes.replace("\n", " | ")[:400])

    base = probe("203.0.113.7", timeout=4)
    ev("steps.txt", "\nbaseline approved: " + json.dumps(base))
    check("baseline approved flow uses the off-link pool", peer_of(base) == "203.0.113.7",
          json.dumps(base))
    base_direct = probe("203.0.113.8", timeout=3)
    ev("steps.txt", "\nbaseline direct: " + json.dumps(base_direct))
    check("baseline unapproved flow is direct", peer_of(base_direct) == "172.31.78.10",
          json.dumps(base_direct))
    exits = [e for e in log_json("/work/logs/exit.log") if e.get("event") == "connect"]
    check("pool exit observed the approved CONNECT", any(e.get("port") == 7110 for e in exits),
          json.dumps(exits[-2:]))
    s_before = sample("before-change")
    check("no egress warning before the change", not s_before["egress_warning"],
          json.dumps(s_before["warnings"]))

    # ------------------------------------------------------------ break 1
    baseline_src = len(log_json("/work/logs/source.log"))
    baseline_exit = len(log_json("/work/logs/exit.log"))
    r = ax("ip link add dummy0 type dummy && ip link set dummy0 up && "
           "ip addr add 10.255.255.1/32 dev dummy0 && "
           f"ip route del default via {ARGS.gw} dev eth0 && "
           "ip route replace default via 10.255.255.1 dev dummy0; ip route")
    ev("steps.txt", "\n$ replace default with dummy0\n" + (r.stdout or "") + (r.stderr or ""))
    check("default-interface change applied", r.returncode == 0 and "dummy0" in (r.stdout or ""),
          (r.stderr or "").strip())
    after = ax("ip route get 172.31.79.20 oif eth0 2>&1; echo ---app; "
               "ip route get 203.0.113.7 2>&1; echo ---arp; "
               "ip neigh flush 172.31.79.20 2>/dev/null; "
               "ip neigh show 172.31.79.20 2>&1").stdout
    ev("steps.txt", "\n$ routes after change\n" + after)
    s_pre = sample("break1-pre-failure")
    check("no egress warning immediately after the route change (before any approved dial)",
          not s_pre["egress_warning"], json.dumps(s_pre["warnings"]))

    approved = probe("203.0.113.7", timeout=6)
    ev("steps.txt", "\nafter change approved: " + json.dumps(approved))
    s_fail = sample("break1-post-failure")
    check("approved flow fails closed after the off-link change", approved.get("ok") is False,
          json.dumps(approved))
    check("status reports the fixed egress warning after the failed approved dial",
          s_fail["egress_warning"],
          json.dumps(s_fail["warnings"]))
    check("the warning text is the fixed destination-free sentence",
          EGRESS_WARNING in s_fail["warnings"],
          json.dumps(s_fail["warnings"]))
    check("tun status stays coverage-only (on, no last_error/detail)",
          s_fail["tun"].get("state") == "on"
          and not s_fail["tun"].get("last_error")
          and not s_fail["tun"].get("detail")
          and EGRESS_WARNING not in s_fail["tun_text"],
          json.dumps({"tun": s_fail["tun"], "text": s_fail["tun_text"]}))
    recs = s_fail["pool_records"]
    check("pool list stays healthy and unchanged by the warning",
          (s_fail["pool_counts"] or {}).get("healthy") == 1
          and all(r.get("health", {}).get("consecutive_failures") == 0 for r in recs),
          json.dumps({"counts": s_fail["pool_counts"], "records": recs}))

    direct = probe("203.0.113.8", timeout=3)
    ev("steps.txt", "\nafter change direct: " + json.dumps(direct))
    new_src = log_json("/work/logs/source.log")[baseline_src:]
    new_exit = log_json("/work/logs/exit.log")[baseline_exit:]
    ev("steps.txt", "\nafter change new source accepts: " + json.dumps(new_src)
       + "\nafter change new exit events: " + json.dumps(new_exit))
    check("no approved flow turned into a direct connection",
          not approved_direct(new_src), json.dumps(approved_direct(new_src)))
    check("no loop: the engine did not reach the pool exit",
          not any(x.get("event") == "connect" for x in new_exit), json.dumps(new_exit[-2:]))
    ev("results.txt", "OBSERVE unapproved direct after change: " + json.dumps(direct))

    # ------------------------------------------------------------ clear-on-success
    r = cli(["tun", "off"])
    ev("steps.txt", "\n$ tun off\n" + (r.stdout or "") + (r.stderr or "") + f"[exit {r.returncode}]")
    check("tun off accepted", r.returncode == 0, f"exit={r.returncode}")
    check("link removed on tun off", wait_for(lambda: not link_present(), 10))
    s_off = sample("break1-after-off")
    check("the warning survives tun off inside its TTL", s_off["egress_warning"],
          json.dumps(s_off["warnings"]))

    r = ax(f"ip route replace default via {ARGS.gw} dev eth0; ip link del dummy0; ip route")
    ev("steps.txt", "\n$ restore default\n" + (r.stdout or "") + (r.stderr or ""))
    r = cli(["tun", "on"])
    ev("steps.txt", "\n$ tun on (restored)\n" + (r.stdout or "") + (r.stderr or "") + f"[exit {r.returncode}]")
    on2 = wait_for(lambda: tun_obj()[0].get("state") == "on", 30)
    check("restored default: tun on", r.returncode == 0 and on2, json.dumps(tun_obj()[0]))
    s_on = sample("restored-before-success")
    check("the warning is still present before a successful approved dial",
          s_on["egress_warning"], json.dumps(s_on["warnings"]))
    approved_ok = probe("203.0.113.7", timeout=5)
    ev("steps.txt", "\nrestored approved: " + json.dumps(approved_ok))
    check("approved pool flow recovers after off/on", peer_of(approved_ok) == "203.0.113.7",
          json.dumps(approved_ok))
    s_clear = sample("restored-after-success")
    check("the first successful approved dial clears the warning",
          not s_clear["egress_warning"], json.dumps(s_clear["warnings"]))
    direct_ok = probe("203.0.113.8", timeout=4)
    ev("steps.txt", "\nrestored direct: " + json.dumps(direct_ok))
    check("direct path recovers after off/on", peer_of(direct_ok) == "172.31.78.10",
          json.dumps(direct_ok))

    # ------------------------------------------------------------ break 2 (TTL)
    r = ax("ip link add dummy0 type dummy && ip link set dummy0 up && "
           "ip addr add 10.255.255.1/32 dev dummy0 && "
           f"ip route del default via {ARGS.gw} dev eth0 && "
           "ip route replace default via 10.255.255.1 dev dummy0; ip route")
    ev("steps.txt", "\n$ replace default with dummy0 (TTL run)\n" + (r.stdout or "") + (r.stderr or ""))
    check("second default-interface change applied", r.returncode == 0 and "dummy0" in (r.stdout or ""),
          (r.stderr or "").strip())
    approved2 = probe("203.0.113.7", timeout=6)
    ev("steps.txt", "\nTTL run approved: " + json.dumps(approved2))
    s_ttl0 = sample("ttl-post-failure")
    check("second approved failure raises the warning",
          approved2.get("ok") is False and s_ttl0["egress_warning"],
          json.dumps({"probe": approved2, "warnings": s_ttl0["warnings"]}))
    failed_at = time.time()
    time.sleep(30)
    s_ttl30 = sample("ttl-30s")
    check("the warning is still present at ~30s", s_ttl30["egress_warning"],
          json.dumps(s_ttl30["warnings"]))
    while time.time() - failed_at < 61:
        time.sleep(1)
    s_ttl61 = sample("ttl-61s")
    check("the warning expires after its 60s TTL with no successful dial",
          not s_ttl61["egress_warning"],
          json.dumps({"age_s": round(time.time() - failed_at, 1),
                      "warnings": s_ttl61["warnings"]}))
    check("tun status remains on through the TTL observation",
          s_ttl61["tun"].get("state") == "on" and not s_ttl61["tun"].get("last_error"),
          json.dumps(s_ttl61["tun"]))

    # ------------------------------------------------------------ final recovery
    r = cli(["tun", "off"])
    ev("steps.txt", "\n$ tun off (final)\n" + (r.stdout or "") + (r.stderr or "") + f"[exit {r.returncode}]")
    wait_for(lambda: not link_present(), 10)
    r = ax(f"ip route replace default via {ARGS.gw} dev eth0; ip link del dummy0; ip route")
    ev("steps.txt", "\n$ restore default (final)\n" + (r.stdout or "") + (r.stderr or ""))
    r = cli(["tun", "on"])
    ev("steps.txt", "\n$ tun on (final)\n" + (r.stdout or "") + (r.stderr or "") + f"[exit {r.returncode}]")
    on3 = wait_for(lambda: tun_obj()[0].get("state") == "on", 30)
    check("final restored default: tun on", r.returncode == 0 and on3, json.dumps(tun_obj()[0]))
    # A persistent failure lets automatic diagnostics cool the exit. That is a
    # separate health action, not the warning; wait for the cooldown before the
    # final recovery probe so the result reflects the restored path.
    until = None
    for rec in pool_obj().get("records") or []:
        value = (rec.get("health") or {}).get("cooldown_until")
        if value:
            when = datetime.datetime.fromisoformat(value.replace("Z", "+00:00"))
            until = when if until is None or when > until else until
    if until is not None:
        delay = (until - datetime.datetime.now(datetime.timezone.utc)).total_seconds() + 2
        ev("steps.txt", f"\nwaiting {delay:.1f}s for the diagnostic pool cooldown")
        if delay > 0:
            time.sleep(delay)
    wait_for(lambda: ((pool_obj().get("counts") or {}).get("usable") or 0) >= 1, 10)
    final_ok = probe("203.0.113.7", timeout=5)
    final_direct = probe("203.0.113.8", timeout=4)
    ev("steps.txt", "\nfinal approved: " + json.dumps(final_ok)
       + "\nfinal direct: " + json.dumps(final_direct))
    check("final approved pool flow recovers", peer_of(final_ok) == "203.0.113.7",
          json.dumps(final_ok))
    check("final direct path recovers", peer_of(final_direct) == "172.31.78.10",
          json.dumps(final_direct))
    s_final = sample("final")

    passed = sum(1 for s, _, _ in RESULTS if s == "PASS")
    failed = sum(1 for s, _, _ in RESULTS if s == "FAIL")
    skipped = sum(1 for s, _, _ in RESULTS if s == "NOTRUN")
    ev("results.txt", f"SUMMARY passed={passed} failed={failed} notrun={skipped} total={len(RESULTS)}")
    return 1 if failed else 0


if __name__ == "__main__":
    raise SystemExit(main())
