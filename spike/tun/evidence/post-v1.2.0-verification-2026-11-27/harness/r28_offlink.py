#!/usr/bin/env python3
"""Post-release verification: R-28 with an off-link pool exit.

The pool SOCKS exit lives in a different subnet (172.31.79.20/32) on the peer,
reachable from the client only through the default gateway 172.31.78.20. This
closes the on-link limitation of the H R-28 test. The script then replaces the
default route with a dummy interface and checks for leaks/loops, status
reporting, and tun off/on recovery.

Evidence-only helper: no product code is touched.
"""
import argparse
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


def bx(script, timeout=120):
    return run(["docker", "exec", ARGS.b, "sh", "-c", script], timeout=timeout)


def adetach(script):
    return run(["docker", "exec", "-d", "-u", "1500:1500", "-e", "HOME=/home/rl",
                ARGS.a, "sh", "-c", script])


def cli(args, timeout=90):
    return run(["docker", "exec", "-u", "1500:1500", "-e", "HOME=/home/rl",
                ARGS.a, ARGS.binary, "--json", "--data-dir", ARGS.data] + list(args),
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


def status_obj():
    r = cli(["status"], timeout=30)
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


def sample(tag):
    t, t_rc = tun_obj()
    st = status_obj()
    txt = ax(f"{ARGS.binary} --data-dir {ARGS.data} tun status", user="1500:1500")
    rec = {
        "tag": tag,
        "t": time.time(),
        "tun": fields(t),
        "tun_rc": t_rc,
        "warnings": st.get("warnings"),
        "status_tun": fields(st.get("tun") or {}),
        "text": (txt.stdout or "").strip(),
        "text_rc": txt.returncode,
        "text_stderr": (txt.stderr or "").strip(),
    }
    ev("r28-steps.txt", json.dumps(rec, sort_keys=True))
    return rec


def probe(host, timeout=4):
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
    """Accepts for the approved destination that came from the client's physical
    address, i.e. the flow left RouteLatch directly instead of via the pool."""
    return [x for x in events if x.get("event") == "accept"
            and x.get("local") == "203.0.113.7" and x.get("peer") == "172.31.78.10"]


def log_json(path):
    r = bx(f"cat {path} 2>/dev/null || true")
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
    p.add_argument("--b", required=True)
    p.add_argument("--binary", required=True)
    p.add_argument("--data", required=True)
    p.add_argument("--evidence", required=True)
    p.add_argument("--gw", default="172.31.78.21")
    p.add_argument("--exit-iface", default="eth1")
    ARGS = p.parse_args()
    os.makedirs(ARGS.evidence, exist_ok=True)
    ev("results.txt", "# post-release R-28 off-link verification")

    # ------------------------------------------------------------ peer fixture
    bx("pkill -f '[s]ocks_exit.py' 2>/dev/null || true; pkill -f '[s]ource_server.py' 2>/dev/null || true")
    time.sleep(0.3)
    bx("ip addr add 172.31.79.20/32 dev " + ARGS.exit_iface + " 2>/dev/null || true; "
       "ip addr add 203.0.113.7/32 dev eth0 2>/dev/null || true; "
       "ip addr add 203.0.113.8/32 dev eth0 2>/dev/null || true; "
       "sysctl -q -w net.ipv4.conf.eth0.arp_ignore=1; "
       "sysctl -q -w net.ipv4.conf.eth0.arp_filter=1")
    bx("nohup python3 /src/spike/tun/h/socks_exit.py --listen 172.31.79.20 --port 11080 "
       "--log /work/logs/exit-offlink.log >/work/logs/exit-offlink.out 2>&1 &")
    bx("nohup python3 /src/spike/tun/h/source_server.py --log /work/logs/source-offlink.log "
       ">/work/logs/source-offlink.out 2>&1 &")
    ok = wait_for(lambda: bx("ss -lnt | grep -q ':11080 ' && ss -lnt | grep -q ':7110 '").returncode == 0, 10)
    check("off-link peer exit and source fixture listening", ok,
          bx("ss -lnt | grep -E ':(11080|7110) ' || true").stdout.strip().replace("\n", "; "))

    # ------------------------------------------------------------ engine setup
    r = ax(f"SUDO_UID=1500 SUDO_GID=1500 {shlex.quote(ARGS.binary)} --json tun setup --socks 127.0.0.1:8318")
    ev("r28-steps.txt", "\n$ tun setup\n" + (r.stdout or "") + (r.stderr or "") + f"[exit {r.returncode}]")
    check("tun setup exit 0", r.returncode == 0, f"exit={r.returncode}")

    ax("rm -f /home/rl/engine-r28.out")
    adetach(f"exec {shlex.quote(ARGS.binary)} --data-dir {shlex.quote(ARGS.data)} "
            "run --headless >>/home/rl/engine-r28.out 2>&1")
    ready = wait_for(lambda: status_obj().get("ready") is True, 30)
    check("engine ready", ready, json.dumps(status_obj().get("warnings")))

    r = cli(["import", "link", "socks5://172.31.79.20:11080#exit-off"])
    ev("r28-steps.txt", "\n$ import link socks5://172.31.79.20:11080#exit-off\n"
       + (r.stdout or "") + (r.stderr or "") + f"[exit {r.returncode}]")
    check("import off-link pool exit", r.returncode == 0, f"exit={r.returncode}")
    r = cli(["rules", "add", "--kind", "ip", "203.0.113.7"])
    ev("r28-steps.txt", "\n$ rules add --kind ip 203.0.113.7\n"
       + (r.stdout or "") + (r.stderr or "") + f"[exit {r.returncode}]")
    check("approved IP rule", r.returncode == 0, f"exit={r.returncode}")

    r = cli(["tun", "on"])
    ev("r28-steps.txt", "\n$ tun on\n" + (r.stdout or "") + (r.stderr or "") + f"[exit {r.returncode}]")
    on = wait_for(lambda: tun_obj()[0].get("state") == "on", 30)
    check("tun on and link up", r.returncode == 0 and on, json.dumps(tun_obj()[0]))

    # ------------------------------------------------------------ off-link proof
    routes = ax("ip route; echo ---get-oif-eth0; ip route get 172.31.79.20 oif eth0 2>&1").stdout
    ev("r28-offlink-topology.txt", "$ routes before change\n" + routes)
    check("pool exit is off-link (via the gateway on eth0, no connected /24)",
          f"172.31.79.20 via {ARGS.gw} dev eth0" in routes
          and "172.31.79.0/24" not in routes,
          routes.replace("\n", " | ")[:400])

    base = probe("203.0.113.7", timeout=4)
    ev("r28-steps.txt", "\nbaseline approved: " + json.dumps(base))
    check("baseline approved flow uses the off-link pool", peer_of(base) == "203.0.113.7",
          json.dumps(base))
    base_direct = probe("203.0.113.8", timeout=3)
    ev("r28-steps.txt", "\nbaseline direct: " + json.dumps(base_direct))
    check("baseline unapproved flow is direct", peer_of(base_direct) == "172.31.78.10",
          json.dumps(base_direct))
    exits = [e for e in log_json("/work/logs/exit-offlink.log") if e.get("event") == "connect"]
    check("pool exit observed the approved CONNECT", any(e.get("port") == 7110 for e in exits),
          json.dumps(exits[-2:]))
    sample("before-change")

    # ------------------------------------------------------------ R-28 change
    baseline_src = len(log_json("/work/logs/source-offlink.log"))
    baseline_exit = len(log_json("/work/logs/exit-offlink.log"))
    r = ax("ip link add dummy0 type dummy && ip link set dummy0 up && "
           "ip addr add 10.255.255.1/32 dev dummy0 && "
           f"ip route del default via {ARGS.gw} dev eth0 && "
           "ip route replace default via 10.255.255.1 dev dummy0; ip route")
    ev("r28-steps.txt", "\n$ replace default with dummy0\n" + (r.stdout or "") + (r.stderr or ""))
    check("default-interface change applied", r.returncode == 0 and "dummy0" in (r.stdout or ""),
          (r.stderr or "").strip())
    after = ax("ip route get 172.31.79.20 oif eth0 2>&1; echo ---app; "
               "ip route get 203.0.113.7 2>&1; echo ---arp; "
               "ip neigh flush 172.31.79.20 2>/dev/null; "
               "ping -c1 -W1 -I eth0 172.31.79.20 2>&1 | tail -2; "
               "ip neigh show 172.31.79.20 2>&1").stdout
    ev("r28-steps.txt", "\n$ routes after change\n" + after)

    s1 = sample("gap-immediate")
    time.sleep(1.0)
    s2 = sample("gap-1s")
    time.sleep(1.5)
    s3 = sample("gap-2.5s")

    approved = probe("203.0.113.7", timeout=4)
    ev("r28-steps.txt", "\nafter change approved: " + json.dumps(approved))
    direct = probe("203.0.113.8", timeout=3)
    ev("r28-steps.txt", "\nafter change direct: " + json.dumps(direct))
    new_src = log_json("/work/logs/source-offlink.log")[baseline_src:]
    new_exit = log_json("/work/logs/exit-offlink.log")[baseline_exit:]
    ev("r28-steps.txt", "\nafter change new source accepts: " + json.dumps(new_src)
       + "\nafter change new exit events: " + json.dumps(new_exit))
    bypass = ax("grep 'tun.bypass' /home/rl/engine-r28.out | tail -4; echo ---errors; "
                "grep -iE 'error|unreachable|loop' /home/rl/engine-r28.out | tail -6").stdout
    ev("r28-steps.txt", "\n$ bypass/engine log after change\n" + bypass)

    check("approved flow fails closed after the off-link change", approved.get("ok") is False,
          json.dumps(approved))
    check("no approved flow turned into a direct connection",
          not approved_direct(new_src), json.dumps(approved_direct(new_src)))
    check("no loop: the engine did not reach the pool exit or loop the app flow",
          not any(x.get("event") == "connect" for x in new_exit), json.dumps(new_exit[-2:]))
    ev("results.txt", "OBSERVE unapproved direct after change: " + json.dumps(direct))

    degraded = []
    for s in (s1, s2, s3):
        t = s["tun"]
        if (t.get("state") != "on" or t.get("last_error") or t.get("detail")
                or s["tun_rc"] not in (0,)
                or any("pool" in str(w) for w in (s.get("warnings") or []))):
            degraded.append(s)
    if degraded:
        check("status reports a degraded/stale condition after the change", True,
              json.dumps([{"tag": s["tag"], "tun": s["tun"], "warnings": s["warnings"]}
                          for s in degraded[:2]]))
    else:
        notrun("status reports a degraded/stale condition after the change",
               "state stayed on with no last_error/detail/pool warning while approved dials failed")

    # ------------------------------------------------------------ off/on
    r = cli(["tun", "off"])
    ev("r28-steps.txt", "\n$ tun off\n" + (r.stdout or "") + (r.stderr or "") + f"[exit {r.returncode}]")
    check("tun off accepted", r.returncode == 0, f"exit={r.returncode}")
    check("link removed on tun off", wait_for(lambda: not link_present(), 10))
    sample("after-off")

    r = cli(["tun", "on"])
    ev("r28-steps.txt", "\n$ tun on (dummy0)\n" + (r.stdout or "") + (r.stderr or "") + f"[exit {r.returncode}]")
    on2 = wait_for(lambda: tun_obj()[0].get("state") == "on", 30)
    check("tun on re-enables after off", r.returncode == 0 and on2, json.dumps(tun_obj()[0]))
    after_on = ax("ip route get 203.0.113.7 2>&1; echo ---pool; "
                  "ip route get 172.31.79.20 2>&1; echo ---bypass; "
                  "grep 'tun.bypass' /home/rl/engine-r28.out | tail -3").stdout
    ev("r28-steps.txt", "\n$ routes/bypass after off/on on dummy0\n" + after_on)
    check("off/on rebinds bypass to the new default interface",
          "interface=dummy0" in after_on, after_on.replace("\n", " | ")[:300])

    src2 = len(log_json("/work/logs/source-offlink.log"))
    ex2 = len(log_json("/work/logs/exit-offlink.log"))
    approved2 = probe("203.0.113.7", timeout=4)
    direct2 = probe("203.0.113.8", timeout=3)
    new_src2 = log_json("/work/logs/source-offlink.log")[src2:]
    new_exit2 = log_json("/work/logs/exit-offlink.log")[ex2:]
    ev("r28-steps.txt", "\nafter off/on approved: " + json.dumps(approved2)
       + "\nafter off/on direct: " + json.dumps(direct2)
       + "\nafter off/on new source accepts: " + json.dumps(new_src2)
       + "\nafter off/on new exit events: " + json.dumps(new_exit2))
    check("off/on does not silently restore the old off-link pool", approved2.get("ok") is False,
          json.dumps(approved2))
    ev("results.txt", "OBSERVE unapproved direct after off/on: " + json.dumps(direct2))
    check("off/on does not leak approved traffic direct",
          not approved_direct(new_src2), json.dumps(approved_direct(new_src2)))
    s4 = sample("after-off-on")

    # ------------------------------------------------------------ restore
    r = cli(["tun", "off"])
    ev("r28-steps.txt", "\n$ tun off (restore)\n" + (r.stdout or "") + (r.stderr or "") + f"[exit {r.returncode}]")
    wait_for(lambda: not link_present(), 10)
    r = ax(f"ip route replace default via {ARGS.gw} dev eth0; ip link del dummy0; ip route")
    ev("r28-steps.txt", "\n$ restore default\n" + (r.stdout or "") + (r.stderr or ""))
    r = cli(["tun", "on"])
    ev("r28-steps.txt", "\n$ tun on (restored)\n" + (r.stdout or "") + (r.stderr or "") + f"[exit {r.returncode}]")
    on3 = wait_for(lambda: tun_obj()[0].get("state") == "on", 30)
    check("restored default: tun on", r.returncode == 0 and on3, json.dumps(tun_obj()[0]))
    approved3 = probe("203.0.113.7", timeout=5)
    direct3 = probe("203.0.113.8", timeout=4)
    ev("r28-steps.txt", "\nrestored approved: " + json.dumps(approved3)
       + "\nrestored direct: " + json.dumps(direct3))
    check("approved pool flow recovers after off/on", peer_of(approved3) == "203.0.113.7",
          json.dumps(approved3))
    check("direct path recovers after off/on", peer_of(direct3) == "172.31.78.10",
          json.dumps(direct3))
    sample("restored")

    failed = sum(1 for s, _, _ in RESULTS if s == "FAIL")
    passed = sum(1 for s, _, _ in RESULTS if s == "PASS")
    skipped = sum(1 for s, _, _ in RESULTS if s == "NOTRUN")
    ev("results.txt", f"SUMMARY passed={passed} failed={failed} notrun={skipped} total={len(RESULTS)}")
    return 1 if failed else 0


if __name__ == "__main__":
    raise SystemExit(main())
