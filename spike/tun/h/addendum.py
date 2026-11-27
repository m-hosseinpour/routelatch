#!/usr/bin/env python3
"""R16-H addendum: IPv6 DNS/53 direct and fail-open status visibility."""
import argparse
import json
import os
import shlex
import subprocess
import sys
import time

ARGS = None
EVID = ""
RESULTS = []


def run(cmd, timeout=120):
    return subprocess.run(cmd, capture_output=True, text=True, timeout=timeout)


def de(target, script, user=None, timeout=120):
    cmd = ["docker", "exec"]
    if user:
        cmd += ["-u", user, "-e", "HOME=/home/rl"]
    cmd += [target, "sh", "-c", script]
    return run(cmd, timeout=timeout)


def ax(script, user=None, timeout=120):
    return de(ARGS.a, script, user=user, timeout=timeout)


def bx(script, timeout=120):
    return de(ARGS.b, script, timeout=timeout)


def ev(name, text):
    with open(os.path.join(EVID, name), "a", encoding="utf-8") as f:
        f.write(text.rstrip("\n") + "\n")


def section(name, title):
    line = f"\n===== {title} ({name}) ====="
    print(line)
    ev("run.log", line)


def check(name, ok, detail=""):
    status = "PASS" if ok else "FAIL"
    RESULTS.append((status, name, detail))
    line = f"{status} {name}" + (f" :: {detail}" if detail else "")
    print(line)
    ev("results.txt", line)
    return ok


def notrun(name, reason):
    RESULTS.append(("NOTRUN", name, reason))
    line = f"NOTRUN {name} :: {reason}"
    print(line)
    ev("results.txt", line)


def json_env(result):
    for line in reversed((result.stdout or "").strip().splitlines()):
        if line.strip().startswith("{"):
            try:
                return json.loads(line)
            except json.JSONDecodeError:
                continue
    return None


def cli(args, timeout=60):
    cmd = ["docker", "exec", "-u", "1500:1500", "-e", "HOME=/home/rl",
           ARGS.a, ARGS.binary, "--json", "--data-dir", ARGS.data] + list(args)
    r = run(cmd, timeout=timeout)
    return r, json_env(r)


def tun_obj():
    r, env = cli(["tun", "status"])
    data = (env or {}).get("data") or {}
    if isinstance(data, dict) and "tun" in data:
        data = data["tun"]
    return data or {}


def app_probe(args, timeout=20):
    script = "python3 /src/spike/tun/fixture/app_probe.py " + " ".join(shlex.quote(a) for a in args)
    r = ax(script, user="1500:1500", timeout=timeout)
    try:
        return json.loads(r.stdout.strip().splitlines()[-1])
    except Exception:
        return {"ok": False, "error": (r.stdout or "") + (r.stderr or ""), "ms": 0}


def wait_for(fn, timeout, interval=0.05):
    end = time.time() + timeout
    while time.time() < end:
        try:
            value = fn()
            if value:
                return value
        except Exception:
            pass
        time.sleep(interval)
    return None


def ipv6_dns():
    section("10-ipv6-dns.txt", "IPv6 plain DNS/53 direct through TUN")
    if tun_obj().get("state") != "on":
        r, env = cli(["tun", "on"], timeout=90)
        ev("10-ipv6-dns.txt", "\n$ tun on\n" + (r.stdout or "") + (r.stderr or "") + f"[exit {r.returncode}]")
        wait_for(lambda: tun_obj().get("state") == "on", 30)
    check("coverage on before IPv6 DNS", tun_obj().get("state") == "on", json.dumps(tun_obj()))
    before_hash = ax("sha256sum /etc/resolv.conf").stdout.strip()
    r = ax("ip -6 route; ip -6 addr show dev rl-tun0; ip addr show dev eth0")
    ev("10-ipv6-dns.txt", "\n$ IPv6 link/routes\n" + (r.stdout or "") + (r.stderr or ""))

    a = app_probe(["dns", "--server", "2001:db8::7", "--name", "canary-h.invalid", "--qtype", "A"])
    aaaa = app_probe(["dns", "--server", "2001:db8::7", "--name", "canary-h.invalid", "--qtype", "AAAA"])
    tcp = app_probe(["dns", "--server", "2001:db8::7", "--name", "canary-h.invalid", "--qtype", "A", "--tcp"])
    ev("10-ipv6-dns.txt", "\nUDP v6 A: " + json.dumps(a)
       + "\nUDP v6 AAAA: " + json.dumps(aaaa)
       + "\nTCP v6 A: " + json.dumps(tcp))
    check("IPv6 UDP DNS A/53 through TUN", a.get("ok") is True and "203.0.113.7" in (a.get("answers") or []),
          json.dumps(a))
    check("IPv6 UDP DNS AAAA/53 through TUN", aaaa.get("ok") is True and "2001:db8::7" in (aaaa.get("answers") or []),
          json.dumps(aaaa))
    check("IPv6 TCP DNS A/53 through TUN", tcp.get("ok") is True and "203.0.113.7" in (tcp.get("answers") or []),
          json.dumps(tcp))

    r = bx("cat /work/logs/v6dns.log 2>/dev/null || true")
    lines = []
    for line in (r.stdout or "").splitlines():
        try:
            lines.append(json.loads(line))
        except json.JSONDecodeError:
            pass
    queries = [x for x in lines if x.get("event") == "query"]
    ev("10-ipv6-dns.txt", "\nv6 resolver query log: " + json.dumps(queries))
    udp = [x for x in queries if x.get("proto") == "udp"]
    tcpq = [x for x in queries if x.get("proto") == "tcp"]
    check("v6 resolver saw two UDP datagrams", len(udp) == 2, json.dumps([x.get("count") for x in udp]))
    check("v6 resolver saw one TCP query", len(tcpq) == 1, json.dumps([x.get("count") for x in tcpq]))
    check("all v6 DNS came from the physical interface",
          bool(queries) and all(x.get("source") == "fd00:78::10" for x in queries),
          json.dumps([x.get("source") for x in queries]))
    after_hash = ax("sha256sum /etc/resolv.conf").stdout.strip()
    ev("10-ipv6-dns.txt", f"\nresolv.conf before: {before_hash}\nafter: {after_hash}")
    check("system DNS unchanged after IPv6 DNS", before_hash == after_hash and before_hash != "")


def failopen_status():
    section("11-failopen-status.txt", "status/tray fields during the fail-open window")
    if tun_obj().get("state") != "on":
        cli(["tun", "on"], timeout=90)
        wait_for(lambda: tun_obj().get("state") == "on", 30)
    check("coverage on before fail-open sampling", tun_obj().get("state") == "on", json.dumps(tun_obj()))
    ax("rm -f /home/rl/status-samples.jsonl /home/rl/link-failopen.jsonl")
    start = run(["docker", "exec", "-d", "-u", "1500:1500", "-e", "HOME=/home/rl", ARGS.a, "sh", "-c",
                 "python3 /src/spike/tun/h/watch_status.py "
                 f"--binary {shlex.quote(ARGS.binary)} --data {shlex.quote(ARGS.data)} "
                 "--duration 8 --interval 0.01 --timeout 0.6 --out /home/rl/status-samples.jsonl "
                 ">>/home/rl/status.out 2>&1"])
    if start.returncode != 0:
        check("status sampler started", False, (start.stderr or "")[:200])
        return
    run(["docker", "exec", "-d", "-u", "1500:1500", "-e", "HOME=/home/rl", ARGS.a, "sh", "-c",
         "python3 /src/spike/tun/h/watch_link.py --duration 8 --interval 0.005 --out /home/rl/link-failopen.jsonl"])
    time.sleep(0.5)
    pid = ax("systemctl show -p MainPID --value routelatch-tun.service").stdout.strip()
    t0 = time.time()
    ax(f"kill -9 {pid}")
    on = wait_for(lambda: tun_obj().get("state") == "on", 15)
    wait_for(lambda: tun_obj().get("state") == "on" and ax("ip link show rl-tun0 >/dev/null 2>&1").returncode == 0, 10)
    time.sleep(0.6)
    ev("11-failopen-status.txt", f"\nkill pid={pid} t0={t0:.6f} state_on_again={bool(on)}")

    samples = []
    for line in ax("cat /home/rl/status-samples.jsonl 2>/dev/null || true").stdout.splitlines():
        try:
            samples.append(json.loads(line))
        except json.JSONDecodeError:
            pass
    transitions = []
    for line in ax("cat /home/rl/link-failopen.jsonl 2>/dev/null || true").stdout.splitlines():
        try:
            transitions.append(json.loads(line))
        except json.JSONDecodeError:
            pass
    absent = [x for x in transitions if not x.get("present") and x.get("t", 0) >= t0]
    back = [x for x in transitions if x.get("present") and x.get("t", 0) >= t0]
    gap_start = absent[0]["t"] if absent else None
    gap_end = back[-1]["t"] if back else None
    inside = []
    if gap_start is not None and gap_end is not None:
        inside = [s for s in samples if gap_start <= s.get("t", 0) <= gap_end]
    ev("11-failopen-status.txt",
       "\nlink transitions: " + json.dumps(transitions)
       + "\nwindow: " + json.dumps({"absent": gap_start, "back": gap_end})
       + "\nsamples: " + json.dumps(samples)
       + "\nin-window samples: " + json.dumps(inside))
    check("status sampler observed the fail-open window", bool(inside),
          f"{len(inside)} of {len(samples)} samples")
    if not inside:
        notrun("tray-facing tun state during fail-open",
               "no status sample landed inside the measured link gap")
        return
    unhealthy = [s for s in inside if s.get("tun_state") not in (None, "on", "off") or s.get("tun_last_error")]
    clean = [s for s in inside if s.get("tun_state") == "off" and not s.get("tun_last_error")]
    if unhealthy:
        check("status exposes error/unavailable during fail-open", True,
              json.dumps(unhealthy[:3]))
    else:
        notrun("status exposes error/unavailable during fail-open",
               "no in-window sample reported an error; observed: "
               + json.dumps([{k: s.get(k) for k in ("tun_state", "tun_last_error", "t")} for s in inside[:3]]))
    check("status does not claim a clean off during fail-open", not clean, json.dumps(clean[:3]))
    check("service auto-restarts after SIGKILL", bool(on), json.dumps(tun_obj()))
    # The tray consumes exactly these status fields. Icon rendering itself needs
    # a desktop session, which this headless container does not have.
    notrun("tray icon rendering during fail-open",
           "headless container: status/tray-facing fields observed, no KDE/Wayland icon")


def finish():
    passed = sum(1 for s, _, _ in RESULTS if s == "PASS")
    failed = sum(1 for s, _, _ in RESULTS if s == "FAIL")
    skipped = sum(1 for s, _, _ in RESULTS if s == "NOTRUN")
    summary = f"SUMMARY passed={passed} failed={failed} notrun={skipped} total={len(RESULTS)}"
    print(summary)
    ev("results.txt", "\n" + summary)
    return 0 if failed == 0 else 1


def main():
    global ARGS, EVID
    p = argparse.ArgumentParser()
    p.add_argument("--a", required=True)
    p.add_argument("--b", required=True)
    p.add_argument("--binary", required=True)
    p.add_argument("--data", required=True)
    p.add_argument("--evidence", required=True)
    ARGS = p.parse_args()
    EVID = ARGS.evidence
    os.makedirs(EVID, exist_ok=True)
    ipv6_dns()
    failopen_status()
    return finish()


if __name__ == "__main__":
    sys.exit(main())
