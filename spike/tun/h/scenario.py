#!/usr/bin/env python3
"""R16-H A17 acceptance scenario (host side).

Drives the real product CLI/engine inside disposable container A against the
controlled peer fixture in container B. Every action and observation is written
under the evidence directory; assertions are collected into results.txt.

This is acceptance/evidence work. It does not modify product code or host
network state.
"""
import argparse
import json
import os
import re
import shlex
import subprocess
import sys
import time

ARGS = None
RESULTS = []
EVID = ""
T0 = time.monotonic()
LOCAL = {"A": None, "B": None}


# ---------------------------------------------------------------- plumbing
def run(cmd, timeout=120, env=None):
    e = dict(os.environ)
    if env:
        e.update(env)
    return subprocess.run(cmd, capture_output=True, text=True, timeout=timeout, env=e)


def de(target, script, user=None, timeout=120, env=None):
    cmd = ["docker", "exec"]
    if user:
        cmd += ["-u", user, "-e", "HOME=/home/rl"]
    for key, value in (env or {}).items():
        cmd += ["-e", f"{key}={value}"]
    cmd += [target, "sh", "-c", script]
    return run(cmd, timeout=timeout)


def ax(script, user=None, timeout=120, env=None):
    return de(ARGS.a, script, user=user, timeout=timeout, env=env)


def bx(script, timeout=120):
    return de(ARGS.b, script, timeout=timeout)


def ax_ok(script, user=None, timeout=120):
    r = ax(script, user=user, timeout=timeout)
    return r.returncode == 0, r


def ev(name, text):
    path = os.path.join(EVID, name)
    with open(path, "a", encoding="utf-8") as f:
        f.write(text.rstrip("\n") + "\n")


def section(name, title):
    line = f"\n===== {title} ({name}) ====="
    print(line)
    ev("run.log", line)
    return name


def capture(name, title, command, result):
    out = result.stdout or ""
    err = result.stderr or ""
    ev(name, f"\n$ {command}\n{out}{err}\n[exit {result.returncode}]")
    return result


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


def ms(start):
    return int((time.monotonic() - start) * 1000)


def json_env(result):
    text = (result.stdout or "").strip()
    for line in reversed(text.splitlines()):
        line = line.strip()
        if line.startswith("{"):
            try:
                return json.loads(line)
            except json.JSONDecodeError:
                continue
    return None


def cli(args, root=False, data=True, timeout=90):
    cmd = ["docker", "exec"]
    if not root:
        cmd += ["-u", "1500:1500", "-e", "HOME=/home/rl"]
    cmd += [ARGS.a, ARGS.binary, "--json"]
    if data:
        cmd += ["--data-dir", ARGS.data]
    cmd += list(args)
    r = run(cmd, timeout=timeout)
    return r, json_env(r)


def cli_ev(name, args, root=False, data=True, timeout=90):
    r, env = cli(args, root=root, data=data, timeout=timeout)
    ev(name, "\n$ routelatch " + " ".join(args) + f"\n{(r.stdout or '')}{(r.stderr or '')}[exit {r.returncode}]")
    return r, env


def cli_ok(name, args, **kw):
    r, env = cli_ev(name, args, **kw)
    ok = r.returncode == 0 and env is not None and env.get("ok") is True
    check(name + ": " + " ".join(args), ok, f"exit={r.returncode}")
    return env


def status(data_name="05-status.txt"):
    r, env = cli(["status"])
    if data_name:
        ev(data_name, "\n$ routelatch status\n" + (r.stdout or "") + (r.stderr or ""))
    return (env or {}).get("data") or {}


def tun_obj(data_name=None):
    r, env = cli(["tun", "status"])
    if data_name:
        ev(data_name, "\n$ routelatch tun status\n" + (r.stdout or "") + (r.stderr or ""))
    data = (env or {}).get("data") or {}
    if isinstance(data, dict) and "tun" in data:
        data = data["tun"]
    return data or {}


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


def engine_pid():
    r = ax("pgrep -u 1500 -f 'run --headless' | head -1")
    if r.returncode != 0:
        return None
    try:
        return int(r.stdout.strip())
    except ValueError:
        return None


def engine_pid_for(data_dir):
    """Find the headless engine whose command line names this data dir."""
    r = ax("pgrep -u 1500 -f 'run --headless' || true")
    for pid in (r.stdout or "").split():
        cmd = ax(f"tr '\\0' ' ' </proc/{pid}/cmdline 2>/dev/null || true").stdout
        if data_dir in cmd:
            try:
                return int(pid)
            except ValueError:
                return None
    return None


def cli_data(data_dir, args, timeout=90):
    cmd = ["docker", "exec", "-u", "1500:1500", "-e", "HOME=/home/rl",
           ARGS.a, ARGS.binary, "--json", "--data-dir", data_dir]
    cmd += list(args)
    r = run(cmd, timeout=timeout)
    return r, json_env(r)


def status_at(data_dir, timeout=30):
    r, env = cli_data(data_dir, ["status"], timeout=timeout)
    return (env or {}).get("data") or {}


def tun_at(data_dir):
    r, env = cli_data(data_dir, ["tun", "status"])
    data = (env or {}).get("data") or {}
    if isinstance(data, dict) and "tun" in data:
        data = data["tun"]
    return data or {}


def start_engine_at(data_dir, logpath, timeout=30):
    ax(f"rm -f {logpath}")
    cmd = (f"exec {shlex.quote(ARGS.binary)} --data-dir {shlex.quote(data_dir)} "
           f"run --headless >>{logpath} 2>&1")
    r = run(["docker", "exec", "-d", "-u", "1500:1500", "-e", "HOME=/home/rl",
             ARGS.a, "sh", "-c", cmd])
    if r.returncode != 0:
        return False
    return bool(wait_for(lambda: status_at(data_dir).get("ready") is True, timeout))



def start_engine(timeout=30):
    ax("rm -f /home/rl/engine.out")
    cmd = (f"exec {shlex.quote(ARGS.binary)} --data-dir {shlex.quote(ARGS.data)} "
           "run --headless >>/home/rl/engine.out 2>&1")
    r = run(["docker", "exec", "-d", "-u", "1500:1500", "-e", "HOME=/home/rl", ARGS.a, "sh", "-c", cmd])
    if r.returncode != 0:
        return False
    ok = wait_for(lambda: status("") and status("").get("ready") is True, timeout)
    ev("04-engine-start.txt", f"\nstart: {cmd}\npid={engine_pid()} ready={bool(ok)}\n" + engine_out())
    if not ok:
        r, env = cli(["status"])
        ev("04-engine-start.txt", "\nstatus diagnostic:\n" + (r.stdout or "") + (r.stderr or "")
           + f"\n[exit {r.returncode}]")
    return bool(ok)


def engine_out():
    r = ax("cat /home/rl/engine.out 2>/dev/null || true")
    return r.stdout or ""


def stop_engine(sig="TERM", timeout=20):
    pid = engine_pid()
    if pid is None:
        return True
    ax(f"kill -{sig} {pid}")
    end = time.time() + timeout
    while time.time() < end:
        if engine_pid() is None:
            return True
        time.sleep(0.05)
    return False


def link_present():
    return ax("ip link show rl-tun0 >/dev/null 2>&1").returncode == 0


def route_count():
    v4 = ax("ip route | grep -c '^0\\.0\\.0\\.0/1 \\|^128\\.0\\.0\\.0/1 ' || true").stdout.strip()
    v6 = ax("ip -6 route | grep -c '^::/1 \\|^8000::/1 ' || true").stdout.strip()
    return (int(v4 or 0), int(v6 or 0))


def source_probe(host, port=7110, timeout=4):
    script = (f"python3 /src/spike/tun/h/app_source.py --host {shlex.quote(host)} "
              f"--port {port} --timeout {timeout}")
    r = ax(script, user="1500:1500", timeout=timeout + 10)
    try:
        return json.loads(r.stdout.strip().splitlines()[-1])
    except Exception:
        return {"ok": False, "error": (r.stdout or "") + (r.stderr or ""), "ms": 0}


def app_probe(args, timeout=20):
    script = "python3 /src/spike/tun/fixture/app_probe.py " + " ".join(shlex.quote(a) for a in args)
    r = ax(script, user="1500:1500", timeout=timeout)
    try:
        return json.loads(r.stdout.strip().splitlines()[-1])
    except Exception:
        return {"ok": False, "error": (r.stdout or "") + (r.stderr or ""), "ms": 0}


def peer_lines():
    r = bx("cat /work/logs/peer.log 2>/dev/null || true", timeout=30)
    out = []
    for line in (r.stdout or "").splitlines():
        try:
            out.append(json.loads(line))
        except json.JSONDecodeError:
            pass
    return out


def exit_lines():
    r = bx("cat /work/logs/exit.log 2>/dev/null || true", timeout=30)
    out = []
    for line in (r.stdout or "").splitlines():
        try:
            out.append(json.loads(line))
        except json.JSONDecodeError:
            pass
    return out


def source_lines():
    r = bx("cat /work/logs/source.log 2>/dev/null || true", timeout=30)
    out = []
    for line in (r.stdout or "").splitlines():
        try:
            out.append(json.loads(line))
        except json.JSONDecodeError:
            pass
    return out


def udp_datagrams():
    r = bx("grep -c '\"event\": \"udp_datagram\"' /work/logs/udp.log 2>/dev/null || true", timeout=30)
    try:
        return int((r.stdout or "0").strip())
    except ValueError:
        return 0


def dns_lines():
    r = bx("cat /work/logs/dns.log 2>/dev/null || true", timeout=30)
    return (r.stdout or "").splitlines()


def last_exit(predicate=lambda e: True):
    for e in reversed(exit_lines()):
        if predicate(e):
            return e
    return None


def last_peer(kind=None, pred=lambda e: True):
    for e in reversed(peer_lines()):
        if (kind is None or e.get("event") == kind) and pred(e):
            return e
    return None


def source_peers_for(local):
    return [e.get("peer") for e in source_lines() if e.get("event") == "accept" and e.get("local") == local]


def wait_engine_ready(timeout=30):
    return wait_for(lambda: status("").get("ready") is True, timeout)


# ---------------------------------------------------------------- scenario
def main():
    global ARGS, EVID
    p = argparse.ArgumentParser()
    p.add_argument("--a", required=True)
    p.add_argument("--b", required=True)
    p.add_argument("--binary", required=True)
    p.add_argument("--data", required=True)
    p.add_argument("--evidence", required=True)
    p.add_argument("--repo", required=True)
    ARGS = p.parse_args()
    EVID = ARGS.evidence
    os.makedirs(EVID, exist_ok=True)
    ev("results.txt", "# R16-H A17 acceptance results")
    print("R16-H acceptance start", flush=True)

    setup_helper()
    start_local_fixture()
    if not start_engine():
        check("engine start", False, "engine did not become ready")
        finish()
        return 1
    check("engine start", True)

    configure_policy()
    enable_tun()
    local_lan_checks()
    targeted_flows()
    mode_checks()
    ipv6_checks()
    dns_udp_checks()
    engine_failclosed()
    service_restart()
    sigkill_failopen()
    tun_off_and_quit()
    persistence()
    two_engine_peer()
    start_limit_bound()
    network_change()
    d21_scan()
    socket_link_witness()
    uninstall_and_helper_missing()
    final_cleanup()
    return finish()


def setup_helper():
    section("02-setup.txt", "helper setup")
    # sudo would set SUDO_UID; the container has no sudo, so run the real binary
    # as root with the same environment contract.
    r = ax(f"SUDO_UID=1500 SUDO_GID=1500 {shlex.quote(ARGS.binary)} --json tun setup --socks 127.0.0.1:8318", timeout=90)
    env = json_env(r)
    ev("02-setup.txt", "\n$ SUDO_UID=1500 routelatch --json tun setup --socks 127.0.0.1:8318\n"
       + (r.stdout or "") + (r.stderr or "") + f"\n[exit {r.returncode}]")
    check("tun setup exit 0", r.returncode == 0 and (env or {}).get("ok") is True, f"exit={r.returncode}")
    files = "/etc/systemd/system/routelatch-tun.service /etc/systemd/system/routelatch-tun.socket " \
            "/etc/routelatch/tun.json /etc/tmpfiles.d/routelatch-tun.conf /usr/libexec/routelatch/tun-service"
    r = ax("stat -c '%U %G %a %n' " + files + " /run/routelatch-tun /run/routelatch-tun/control.sock")
    ev("02-setup.txt", "\n$ stat ...\n" + (r.stdout or "") + (r.stderr or ""))
    check("setup layout root-owned", r.returncode == 0 and "root root 755 /usr/libexec/routelatch/tun-service" in (r.stdout or ""))
    check("control socket 0600/uid1500", "rl rl 600 /run/routelatch-tun/control.sock" in (r.stdout or "")
          or "1500 1500 600 /run/routelatch-tun/control.sock" in (r.stdout or ""))
    r = ax("systemctl is-active routelatch-tun.socket; stat -c '%a %u %g' /run/routelatch-tun")
    ev("02-setup.txt", "\n$ socket/runtime\n" + (r.stdout or "") + (r.stderr or ""))
    check("socket active", (r.stdout or "").strip().splitlines()[0] == "active")

    r = ax("grep -E '^(BindsTo|StartLimitIntervalSec|StartLimitBurst|StartLimitAction)=' "
           "/etc/systemd/system/routelatch-tun.service")
    ev("02-setup.txt", "\n$ helper unit limits\n" + (r.stdout or "") + (r.stderr or ""))
    limits = r.stdout or ""
    check("unit start limit 20/300/none", "StartLimitIntervalSec=300" in limits
          and "StartLimitBurst=20" in limits and "StartLimitAction=none" in limits, limits.strip())
    check("service binds to its socket", "BindsTo=routelatch-tun.socket" in limits, limits.strip())

    # Idempotent second setup.
    r = ax(f"SUDO_UID=1500 {shlex.quote(ARGS.binary)} --json tun setup --socks 127.0.0.1:8318", timeout=90)
    env = json_env(r)
    data = (env or {}).get("data") or {}
    ev("02-setup.txt", "\n$ second setup\n" + (r.stdout or "") + (r.stderr or "") + f"[exit {r.returncode}]")
    check("setup idempotent", r.returncode == 0 and data.get("changed") is False, json.dumps(data))

    # Direct root without SUDO_UID must be refused before any write.
    r = ax(f"env -u SUDO_UID {shlex.quote(ARGS.binary)} --json tun setup", timeout=30)
    env = json_env(r) or {}
    err = env.get("error") or {}
    ev("02-setup.txt", "\n$ env -u SUDO_UID routelatch tun setup\n" + (r.stdout or "") + (r.stderr or "") + f"[exit {r.returncode}]")
    check("setup refuses root without SUDO_UID", r.returncode == 9 and err.get("kind") == "policy_denied", f"exit={r.returncode}")


def start_local_fixture():
    ax("pkill -u 1500 -f 'source_server.py --log /home/rl/local-source.log' 2>/dev/null || true")
    ax("nohup python3 /src/spike/tun/h/source_server.py --log /home/rl/local-source.log "
       ">/home/rl/local-source.out 2>&1 &", user="1500:1500")
    wait_for(lambda: ax("ss -lnt | grep -q ':7110 '").returncode == 0, 5)


def configure_policy():
    section("03-policy.txt", "policy: pool + approved rules")
    r, _ = cli_ev("03-policy.txt", ["tun", "status"])
    ev("03-policy.txt", "\n$ tun status before on\n" + (r.stdout or "") + (r.stderr or ""))
    r, env = cli_ev("03-policy.txt", ["import", "link", "socks5://172.31.77.20:11080#exit-h"])
    check("import pool exit", r.returncode == 0 and (env or {}).get("ok") is True, f"exit={r.returncode}")
    for host in ["canary-h.invalid", "canary6-h.invalid", "canary-http-h.invalid"]:
        r, env = cli_ev("03-policy.txt", ["rules", "add", "--kind", "domain", host])
        check("domain rule " + host, r.returncode == 0 and (env or {}).get("ok") is True, f"exit={r.returncode}")
    st = status("03-policy.txt")
    ev("03-policy.txt", "\nstatus after policy: " + json.dumps(st, sort_keys=True))
    check("pool has one record", (st.get("pool") or {}).get("total") == 1, json.dumps(st.get("pool")))
    check("targeted mode", st.get("mode") == "targeted", str(st.get("mode")))


def enable_tun():
    section("04-enable.txt", "tun on and dual-stack routes")
    r, env = cli_ev("04-enable.txt", ["tun", "on"], timeout=90)
    check("tun on accepted", r.returncode == 0 and (env or {}).get("ok") is True, f"exit={r.returncode}")
    on = wait_for(lambda: tun_obj().get("state") == "on", 30)
    check("tun state on", bool(on), json.dumps(tun_obj()))
    st = status("04-enable.txt")
    tun = st.get("tun") or {}
    ev("04-enable.txt", "\nstatus: " + json.dumps(st, sort_keys=True))
    check("status tun desired/on", tun.get("desired") is True and tun.get("state") == "on", json.dumps(tun))
    check("dns direct + udp blocked + ipv6", tun.get("dns") == "direct" and tun.get("udp") == "blocked"
          and tun.get("ipv6") is True, json.dumps(tun))
    r = ax("ip -br addr show rl-tun0; echo ---; ip route | grep -E '^(0\\.0\\.0\\.0/1|128\\.0\\.0\\.0/1|198\\.18\\.)'; "
           "echo ---; ip -6 route | grep -E '^(::/1|8000::/1|fc00::)'; echo ---; cat /run/routelatch-tun/lease.json")
    ev("04-enable.txt", "\n$ link/routes/lease\n" + (r.stdout or "") + (r.stderr or ""))
    check("dual-stack link", "198.18.0.1/15" in (r.stdout or "") and "fc00::1/64" in (r.stdout or ""))
    v4, v6 = route_count()
    check("IPv4 half routes", v4 == 2, f"count={v4}")
    check("IPv6 half routes", v6 == 2, f"count={v6}")
    check("lease desired on", '"desired": "on"' in (r.stdout or "") or '"desired":"on"' in (r.stdout or ""), "")
    r = ax("systemctl show routelatch-tun.service -p MainPID -p User -p AmbientCapabilities -p CapabilityBoundingSet; "
           "P=$(systemctl show -p MainPID --value routelatch-tun.service); "
           "grep -E '^(Uid|Gid|CapEff):' /proc/$P/status; readlink /proc/$P/exe")
    ev("04-enable.txt", "\n$ service identity\n" + (r.stdout or "") + (r.stderr or ""))
    check("service non-root net_admin", "Uid:\t1500" in (r.stdout or "") and "cap_net_admin" in (r.stdout or "").lower()
          and "0000000000001000" in (r.stdout or ""))


def local_lan_checks():
    section("13-local-lan.txt", "localhost and LAN stay direct")
    r = source_probe("127.0.0.1")
    ev("13-local-lan.txt", "\nlocalhost source: " + json.dumps(r))
    check("localhost direct", r.get("ok") and json.loads(r["raw"]).get("peer") == "127.0.0.1", json.dumps(r))
    r = source_probe("172.31.77.20")
    ev("13-local-lan.txt", "\nLAN source: " + json.dumps(r))
    check("LAN direct", r.get("ok") and json.loads(r["raw"]).get("peer") == "172.31.77.10", json.dumps(r))


def targeted_flows():
    section("06-targeted-flows.txt", "targeted app flows: sniff, IP rule, direct")
    # Domain-only rules: a successful TLS/HTTP flow proves sniffing, because the
    # IP 203.0.113.7 is not approved yet.
    tls = app_probe(["tcp", "--host", "203.0.113.7", "--port", "7101", "--mode", "tls",
                     "--sni", "canary-h.invalid", "--timeout", "6"])
    ev("06-targeted-flows.txt", "\nTLS domain: " + json.dumps(tls))
    e = last_exit(lambda x: x.get("event") == "connect" and x.get("port") == 7101)
    check("TLS through TUN succeeds", tls.get("ok") is True, json.dumps(tls))
    check("TLS SNI reaches SOCKS (sniff)", e is not None and e.get("target") == "canary-h.invalid", json.dumps(e))
    p = last_peer("tls_request", lambda x: x.get("sni") == "canary-h.invalid")
    check("peer sees TLS SNI", p is not None, json.dumps(p))

    http = app_probe(["tcp", "--host", "203.0.113.7", "--port", "7104", "--mode", "http",
                      "--host-header", "canary-http-h.invalid", "--timeout", "6"])
    ev("06-targeted-flows.txt", "\nHTTP domain: " + json.dumps(http))
    e = last_exit(lambda x: x.get("event") == "connect" and x.get("port") == 7104)
    check("HTTP through TUN succeeds", http.get("ok") is True, json.dumps(http))
    check("HTTP Host reaches SOCKS (sniff)", e is not None and e.get("target") == "canary-http-h.invalid", json.dumps(e))

    # Approved IP rule: echo flow must use the pool exit.
    cli_ev("06-targeted-flows.txt", ["rules", "add", "--kind", "ip", "203.0.113.7"])
    before = len([x for x in exit_lines() if x.get("event") == "connect"])
    r = source_probe("203.0.113.7")
    ev("06-targeted-flows.txt", "\napproved IP source: " + json.dumps(r))
    peer = json.loads(r["raw"]).get("peer") if r.get("ok") else None
    check("approved IP uses pool", peer == "203.0.113.7", f"peer={peer}")

    # Unapproved IP must use the engine's direct dial, bound to eth0.
    before_exit = len([x for x in exit_lines() if x.get("event") == "connect"])
    r = source_probe("203.0.113.8")
    ev("06-targeted-flows.txt", "\nunapproved IP source: " + json.dumps(r))
    peer = json.loads(r["raw"]).get("peer") if r.get("ok") else None
    check("unapproved IP goes direct", peer == "172.31.77.10", f"peer={peer}")
    after_exit = len([x for x in exit_lines() if x.get("event") == "connect"])
    check("direct flow does not enter pool", after_exit == before_exit, f"{before_exit}->{after_exit}")
    st = status("06-targeted-flows.txt")
    check("engine remains ready", st.get("ready") is True, json.dumps(st.get("warnings")))


def mode_checks():
    section("07-modes.txt", "all-proxy / all-direct / stopped and D13 sweep")
    cli_ev("07-modes.txt", ["mode", "all-proxy"])
    st = wait_for(lambda: status("").get("mode") == "all-proxy" and status(""), 10)
    check("mode all-proxy", bool(st), json.dumps(st))
    r = source_probe("203.0.113.8")
    ev("07-modes.txt", "\nall-proxy unapproved source: " + json.dumps(r))
    peer = json.loads(r["raw"]).get("peer") if r.get("ok") else None
    check("all-proxy uses pool for unapproved", peer == "203.0.113.8", f"peer={peer}")

    # D13 sweep: a live pooled connection is closed by the mode change.
    cli_ev("07-modes.txt", ["mode", "targeted"])
    wait_for(lambda: status("").get("mode") == "targeted", 10)
    ax("rm -f /home/rl/hold.out")
    baseline = len([x for x in exit_lines() if x.get("event") == "connect" and x.get("port") == 7102])
    run(["docker", "exec", "-d", "-u", "1500:1500", "-e", "HOME=/home/rl", ARGS.a, "sh", "-c",
         "python3 /src/spike/tun/h/hold_conn.py --host 203.0.113.7 --port 7102 --timeout 12 "
         ">>/home/rl/hold.out 2>&1"])
    connected = wait_for(lambda: "connected" in ax("cat /home/rl/hold.out 2>/dev/null || true").stdout, 5)
    registered = wait_for(lambda: len([x for x in exit_lines()
                                       if x.get("event") == "connect" and x.get("port") == 7102]) > baseline, 5)
    ev("07-modes.txt", "\nhold before switch: " + ax("cat /home/rl/hold.out 2>/dev/null || true").stdout
       + f"\nregistered={bool(registered)}")
    check("held connection established", bool(connected), ax("cat /home/rl/hold.out 2>/dev/null || true").stdout.strip())
    check("held connection is tracked before the switch", bool(registered), "")
    t = time.monotonic()
    cli_ev("07-modes.txt", ["mode", "all-direct"])
    wait_for(lambda: status("").get("mode") == "all-direct", 10)
    closed = wait_for(lambda: any(word in ax("cat /home/rl/hold.out 2>/dev/null || true").stdout
                                  for word in ("closed", "still-open", "connect-error")), 6)
    hold_out = ax("cat /home/rl/hold.out 2>/dev/null || true").stdout
    ev("07-modes.txt", "\nhold after switch: " + hold_out + "\nmode switch elapsed ms: " + str(ms(t)))
    check("mode switch sweeps the held flow", closed and "closed" in hold_out, hold_out.strip())
    # A fresh connection in all-direct must be direct.
    r = source_probe("203.0.113.7")
    peer = json.loads(r["raw"]).get("peer") if r.get("ok") else None
    check("all-direct uses direct egress", peer == "172.31.77.10", f"peer={peer}")

    # Stopped mode closes coverage and listeners, and refuses tun on.
    before = len(exit_lines())
    cli_ev("07-modes.txt", ["mode", "stopped"])
    wait_for(lambda: status("").get("mode") == "stopped", 10)
    wait_for(lambda: not link_present(), 10)
    st = status("07-modes.txt")
    ev("07-modes.txt", "\nstopped status: " + json.dumps(st, sort_keys=True))
    check("stopped closes TUN", not link_present() and (st.get("tun") or {}).get("state") == "off",
          json.dumps(st.get("tun")))
    check("stopped closes listeners", st.get("listeners_open") is False, json.dumps(st.get("listeners")))
    r = source_probe("203.0.113.7")
    peer = json.loads(r["raw"]).get("peer") if r.get("ok") else None
    check("stopped app goes direct", peer == "172.31.77.10", f"peer={peer}")
    r, env = cli_ev("07-modes.txt", ["tun", "on"])
    check("tun on refused in stopped", r.returncode == 3 and ((env or {}).get("error") or {}).get("kind") == "stopped",
          f"exit={r.returncode}")

    # A process that entered stopped mode cannot leave it without a restart
    # (contracts §2.7/§10.1). Restart, then resume targeted from the persisted
    # stopped state: the saved TUN desire must take effect again.
    if not stop_engine("TERM"):
        check("engine restart after stopped", False)
        return
    if not start_engine():
        check("engine restart after stopped", False)
        return
    check("engine restart after stopped", wait_engine_ready(20))
    cli_ev("07-modes.txt", ["mode", "targeted"])
    wait_for(lambda: status("").get("mode") == "targeted", 10)
    on = wait_for(lambda: tun_obj().get("state") == "on", 30)
    check("targeted restores coverage after restart", bool(on), json.dumps(tun_obj()))


def ipv6_checks():
    section("08-ipv6.txt", "IPv6 app flows")
    cli_ev("08-ipv6.txt", ["rules", "add", "--kind", "ip", "2001:db8::7"])
    tls = app_probe(["tcp", "--host", "2001:db8::7", "--port", "7101", "--mode", "tls",
                     "--sni", "canary6-h.invalid", "--timeout", "6"])
    ev("08-ipv6.txt", "\nIPv6 TLS: " + json.dumps(tls))
    e = last_exit(lambda x: x.get("event") == "connect" and x.get("port") == 7101)
    check("IPv6 TLS succeeds", tls.get("ok") is True, json.dumps(tls))
    check("IPv6 SNI reaches SOCKS", e is not None and e.get("target") == "canary6-h.invalid", json.dumps(e))
    p = last_peer("tls_request", lambda x: x.get("sni") == "canary6-h.invalid")
    check("peer sees IPv6 SNI", p is not None, json.dumps(p))
    r = source_probe("2001:db8::7")
    peer = json.loads(r["raw"]).get("peer") if r.get("ok") else None
    check("IPv6 approved uses pool", peer == "2001:db8::7", f"peer={peer}")
    r = source_probe("2001:db8::8")
    peer = json.loads(r["raw"]).get("peer") if r.get("ok") else None
    check("IPv6 unapproved goes direct", peer == "fd00:77::10", f"peer={peer}")


def dns_udp_checks():
    section("09-dns-udp.txt", "DNS/53 direct, non-53 UDP blocked")
    before_hash = ax("sha256sum /etc/resolv.conf").stdout.strip()
    a = app_probe(["dns", "--server", "203.0.113.7", "--name", "canary-h.invalid", "--qtype", "A"])
    aaaa = app_probe(["dns", "--server", "203.0.113.7", "--name", "canary-h.invalid", "--qtype", "AAAA"])
    tcp = app_probe(["dns", "--server", "203.0.113.7", "--name", "canary-h.invalid", "--qtype", "A", "--tcp"])
    ev("09-dns-udp.txt", "\nDNS A: " + json.dumps(a) + "\nDNS AAAA: " + json.dumps(aaaa) + "\nDNS TCP: " + json.dumps(tcp))
    check("DNS A/53 through TUN", a.get("ok") is True and "203.0.113.7" in (a.get("answers") or []), json.dumps(a))
    check("DNS AAAA/53 through TUN", aaaa.get("ok") is True and "2001:db8::7" in (aaaa.get("answers") or []), json.dumps(aaaa))
    check("DNS TCP/53 through TUN", tcp.get("ok") is True, json.dumps(tcp))
    lines = dns_lines()
    direct = [l for l in lines if "from 172.31.77.10" in l]
    ev("09-dns-udp.txt", "\ndnsmasq lines with physical source: " + str(direct[-3:]))
    check("DNS reaches peer from physical iface", bool(direct), str(direct[-1:]))

    udp_before = udp_datagrams()
    u1 = app_probe(["udp", "--host", "203.0.113.7", "--port", "9999", "--payload", "RLH-UDP-NON53"])
    u2 = app_probe(["udp", "--host", "203.0.113.7", "--port", "443", "--payload", "RLH-QUIC"])
    u3 = app_probe(["udp", "--host", "2001:db8::7", "--port", "9999", "--payload", "RLH-UDP6"])
    time.sleep(0.5)
    udp_after = udp_datagrams()
    ev("09-dns-udp.txt", "\nUDP v4/9999: " + json.dumps(u1) + "\nUDP v4/443: " + json.dumps(u2)
       + "\nUDP v6/9999: " + json.dumps(u3) + f"\npeer datagrams: {udp_before}->{udp_after}")
    check("non-53 UDP blocked v4", u1.get("ok") is False and "timeout" in str(u1.get("error")))
    check("QUIC/443 blocked v4", u2.get("ok") is False and "timeout" in str(u2.get("error")))
    check("non-53 UDP blocked v6", u3.get("ok") is False and "timeout" in str(u3.get("error")))
    check("zero UDP datagrams at peer", udp_after == udp_before, f"{udp_before}->{udp_after}")
    after_hash = ax("sha256sum /etc/resolv.conf").stdout.strip()
    ev("09-dns-udp.txt", f"\nresolv.conf before: {before_hash}\nafter: {after_hash}")
    check("system DNS unchanged", before_hash == after_hash and before_hash != "")


def engine_failclosed():
    section("10-engine-failclosed.txt", "engine kill -> fail-closed, restart recovers")
    tun_on = tun_obj().get("state") == "on"
    check("precondition TUN on", tun_on, json.dumps(tun_obj()))
    if not tun_on:
        return
    before_src = len(source_peers_for("203.0.113.7"))
    pid = engine_pid()
    ev("10-engine-failclosed.txt", f"\nengine pid={pid}")
    check("engine pid present", pid is not None)
    ax(f"kill -9 {pid}")
    wait_for(lambda: engine_pid() is None, 10)
    check("engine killed", engine_pid() is None)
    check("TUN link stays after engine death", link_present())
    v4, v6 = route_count()
    check("TUN routes stay after engine death", v4 == 2 and v6 == 2, f"v4={v4} v6={v6}")
    t = time.monotonic()
    r = source_probe("203.0.113.7", timeout=4)
    dur = ms(t)
    ev("10-engine-failclosed.txt", "\nfail-closed probe: " + json.dumps(r) + f" ({dur} ms)")
    check("flow fails closed (no direct reply)", r.get("ok") is False, json.dumps(r))
    after_src = len(source_peers_for("203.0.113.7"))
    check("no direct fallback while engine dead", after_src == before_src, f"{before_src}->{after_src}")

    if not start_engine():
        check("engine restart", False)
        return
    check("engine restart becomes ready", wait_engine_ready(20))
    check("TUN adopted after engine restart", tun_obj().get("state") == "on", json.dumps(tun_obj()))
    r = source_probe("203.0.113.7")
    peer = json.loads(r["raw"]).get("peer") if r.get("ok") else None
    check("flow recovers through pool", peer == "203.0.113.7", f"peer={peer}")


def service_restart():
    section("11-service-restart.txt", "TUN service restart and lease rebuild")
    if tun_obj().get("state") != "on":
        cli_ev("11-service-restart.txt", ["tun", "on"], timeout=90)
        wait_for(lambda: tun_obj().get("state") == "on", 30)
    ax("rm -f /home/rl/link-restart.jsonl")
    run(["docker", "exec", "-d", "-u", "1500:1500", "-e", "HOME=/home/rl", ARGS.a, "sh", "-c",
         "python3 /src/spike/tun/h/watch_link.py --duration 5 --interval 0.005 --out /home/rl/link-restart.jsonl"])
    time.sleep(0.4)
    t0 = time.time()
    r = ax("systemctl restart routelatch-tun.service")
    ev("11-service-restart.txt", "\n$ systemctl restart\n" + (r.stdout or "") + (r.stderr or ""))
    on = wait_for(lambda: tun_obj().get("state") == "on", 15)
    wait_for(lambda: "true" in ax("cat /home/rl/link-restart.jsonl 2>/dev/null || true").stdout, 6)
    transitions = []
    for line in ax("cat /home/rl/link-restart.jsonl 2>/dev/null || true").stdout.splitlines():
        try:
            transitions.append(json.loads(line))
        except json.JSONDecodeError:
            pass
    ev("11-service-restart.txt", "\nlink transitions: " + json.dumps(transitions))
    absent = [x for x in transitions if not x.get("present") and x.get("t", 0) >= t0]
    back = [x for x in transitions if x.get("present") and x.get("t", 0) >= t0]
    check("service restart shows a link gap", bool(absent) and bool(back),
          f"absent={len(absent)} back={len(back)}")
    check("service restart reports on", bool(on), json.dumps(tun_obj()))
    r = source_probe("203.0.113.7")
    peer = json.loads(r["raw"]).get("peer") if r.get("ok") else None
    check("flow recovers after service restart", peer == "203.0.113.7", f"peer={peer}")
    r = ax("cat /run/routelatch-tun/lease.json; stat -c '%a %u' /run/routelatch-tun/control.sock")
    ev("11-service-restart.txt", "\nlease/socket: " + (r.stdout or ""))


def sigkill_failopen():
    section("12-sigkill-failopen.txt", "SIGKILL fail-open measurement")
    if tun_obj().get("state") != "on":
        cli_ev("12-sigkill-failopen.txt", ["tun", "on"], timeout=90)
        wait_for(lambda: tun_obj().get("state") == "on", 30)
    ax("rm -f /home/rl/watch-sigkill.jsonl /home/rl/link-sigkill.jsonl")
    run(["docker", "exec", "-d", "-u", "1500:1500", "-e", "HOME=/home/rl", ARGS.a, "sh", "-c",
         "python3 /src/spike/tun/h/watch_source.py --host 203.0.113.7 --port 7110 "
         "--duration 7 --interval 0.02 --timeout 0.5 --out /home/rl/watch-sigkill.jsonl"])
    run(["docker", "exec", "-d", "-u", "1500:1500", "-e", "HOME=/home/rl", ARGS.a, "sh", "-c",
         "python3 /src/spike/tun/h/watch_link.py --duration 7 --interval 0.005 --out /home/rl/link-sigkill.jsonl"])
    time.sleep(0.7)
    pid = ax("systemctl show -p MainPID --value routelatch-tun.service").stdout.strip()
    t0 = time.time()
    ax(f"kill -9 {pid}")
    on = wait_for(lambda: tun_obj().get("state") == "on", 15)
    wait_for(lambda: tun_obj().get("state") == "on" and link_present(), 10)
    time.sleep(0.5)
    ev("12-sigkill-failopen.txt", f"\nkill pid={pid} t0={t0:.6f} state_on_again={bool(on)}")

    samples = []
    for line in ax("cat /home/rl/watch-sigkill.jsonl 2>/dev/null || true").stdout.splitlines():
        try:
            samples.append(json.loads(line))
        except json.JSONDecodeError:
            pass
    transitions = []
    for line in ax("cat /home/rl/link-sigkill.jsonl 2>/dev/null || true").stdout.splitlines():
        try:
            transitions.append(json.loads(line))
        except json.JSONDecodeError:
            pass
    first_direct = None
    last_direct = None
    first_fail = None
    link_back = None
    for s in samples:
        if s.get("t", 0) < t0:
            continue
        if not s.get("ok"):
            first_fail = first_fail or s["t"]
            continue
        try:
            peer = json.loads(s.get("raw", "{}")).get("peer")
        except Exception:
            peer = None
        if peer == "172.31.77.10":
            first_direct = first_direct or s["t"]
            last_direct = s["t"]
    for x in transitions:
        if x.get("present") and x.get("t", 0) >= t0:
            link_back = x["t"]
    ev("12-sigkill-failopen.txt", "\nlink transitions: " + json.dumps(transitions)
       + "\nfirst_fail_s=" + str(None if first_fail is None else round(first_fail - t0, 3))
       + "\nfirst_direct_s=" + str(None if first_direct is None else round(first_direct - t0, 3))
       + "\nlast_direct_s=" + str(None if last_direct is None else round(last_direct - t0, 3))
       + "\nlink_back_s=" + str(None if link_back is None else round(link_back - t0, 3)))
    check("SIGKILL produces a measured fail-open window", first_direct is not None,
          f"direct observed at {round(first_direct-t0,3)}s" if first_direct else "no direct observation")
    if first_direct is not None:
        check("fail-open window is short and bounded", link_back is not None and (link_back - t0) <= 5,
              f"direct {round(first_direct-t0,3)}s..{round(last_direct-t0,3)}s, "
              f"link back {None if link_back is None else round(link_back-t0,3)}s")
    check("service auto-restarts after SIGKILL", bool(on), json.dumps(tun_obj()))
    r = source_probe("203.0.113.7")
    peer = json.loads(r["raw"]).get("peer") if r.get("ok") else None
    check("flow recovers through pool after SIGKILL", peer == "203.0.113.7", f"peer={peer}")


def tun_off_and_quit():
    section("13-off-quit.txt", "tun off and Quit cleanup")
    r, env = cli_ev("13-off-quit.txt", ["tun", "off"], timeout=90)
    wait_for(lambda: not link_present(), 10)
    st = status("13-off-quit.txt")
    ev("13-off-quit.txt", "\nstatus after off: " + json.dumps(st, sort_keys=True))
    check("tun off removes link", not link_present() and r.returncode == 0)
    check("tun off clears desire", (st.get("tun") or {}).get("desired") is False
          and (st.get("tun") or {}).get("state") == "off", json.dumps(st.get("tun")))
    r = source_probe("203.0.113.7")
    peer = json.loads(r["raw"]).get("peer") if r.get("ok") else None
    check("app direct after tun off", peer == "172.31.77.10", f"peer={peer}")
    udp_before = udp_datagrams()
    app_probe(["udp", "--host", "203.0.113.7", "--port", "9999", "--payload", "RLH-UDP-OFF"])
    time.sleep(0.5)
    udp_after = udp_datagrams()
    check("non-53 UDP allowed after off (proves on-block)", udp_after > udp_before, f"{udp_before}->{udp_after}")

    # Re-enable, then Quit: coverage must close before the engine exits.
    cli_ev("13-off-quit.txt", ["tun", "on"], timeout=90)
    wait_for(lambda: tun_obj().get("state") == "on", 30)
    check("tun on again", tun_obj().get("state") == "on", json.dumps(tun_obj()))
    if not stop_engine("TERM"):
        check("engine SIGTERM exit", False)
    check("Quit removes TUN link", not link_present())
    v4, v6 = route_count()
    check("Quit removes TUN routes", v4 == 0 and v6 == 0, f"v4={v4} v6={v6}")
    check("Quit leaves no engine process", engine_pid() is None)


def persistence():
    section("14-persistence.txt", "desired TUN persists; quick sequence leaves units usable")
    # The quick acceptance sequence (initial enable, manual service restart,
    # SIGKILL restart, toggle and Quit) reached the old StartLimitBurst=5/30s
    # limit. The shipped unit now allows 20 starts per five minutes, so the same
    # natural sequence must leave both units usable and need no reset.
    r = ax("systemctl is-active routelatch-tun.socket; systemctl is-active routelatch-tun.service; "
           "systemctl is-failed routelatch-tun.service; "
           "systemctl show -p Result -p NRestarts routelatch-tun.service; "
           "journalctl -u routelatch-tun.service -n 12 --no-pager")
    ev("14a-start-limit.txt", "\n$ helper state after quick sequence\n" + (r.stdout or "") + (r.stderr or ""))
    out = r.stdout or ""
    hit = "start-limit-hit" in out
    failed = ax("systemctl is-failed routelatch-tun.service 2>/dev/null || true").stdout.strip() == "failed"
    check("quick sequence does not exhaust start limit", not hit and not failed,
          "start-limit-hit" if hit else ("unit failed" if failed else "units usable"))
    socket_active = ax("systemctl is-active routelatch-tun.socket 2>/dev/null || true").stdout.strip()
    check("socket unit stays usable after quick sequence", socket_active == "active", socket_active)
    if hit or failed:
        notrun("quick sequence unit recovery", "limit/failed state needed the documented reset")
        ax("systemctl reset-failed routelatch-tun.socket routelatch-tun.service 2>/dev/null || true; "
           "systemctl start routelatch-tun.socket 2>/dev/null || true")
        time.sleep(1)
    if not start_engine():
        check("engine restart for persistence", False)
        return
    check("engine restart ready", wait_engine_ready(20))
    on = wait_for(lambda: tun_obj().get("state") == "on", 30)
    if not on:
        r = ax("systemctl is-active routelatch-tun.socket; systemctl is-active routelatch-tun.service; "
               "systemctl show -p NRestarts -p Result -p ExecMainStatus routelatch-tun.service; "
               "journalctl -u routelatch-tun.service -n 15 --no-pager")
        ev("14-persistence.txt", "\n$ helper state after restart\n" + (r.stdout or "") + (r.stderr or ""))
    check("TUN auto-enables from saved desire", bool(on), json.dumps(tun_obj()))
    r = source_probe("203.0.113.7")
    peer = json.loads(r["raw"]).get("peer") if r.get("ok") else None
    check("auto-enabled coverage passes app flow", peer == "203.0.113.7", f"peer={peer}")


def two_engine_peer():
    section("19-two-engine.txt", "second engine keeps peer-owned coverage")
    if tun_obj().get("state") != "on":
        cli_ev("19-two-engine.txt", ["tun", "on"], timeout=90)
        wait_for(lambda: tun_obj().get("state") == "on", 30)
    check("first engine coverage on for peer test", tun_obj().get("state") == "on", json.dumps(tun_obj()))

    data2 = "/home/rl/data2"
    ax(f"install -d -m 700 -o rl -g rl {data2}")
    # Config changes require the running service; seed the second data dir from
    # the first config instead, with distinct loopback listeners.
    ax(f"cp {ARGS.data}/config.json {data2}/config.json && chown rl:rl {data2}/config.json && "
       f"chmod 600 {data2}/config.json && "
       f"sed -i 's/127\\.0\\.0\\.1:8317/127.0.0.1:8417/; s/127\\.0\\.0\\.1:8318/127.0.0.1:8418/' "
       f"{data2}/config.json")
    r = ax(f"grep -E '\"(http_listen|socks_listen|tun_enabled)\"' {data2}/config.json")
    ev("19-two-engine.txt", "\n$ second engine config\n" + (r.stdout or "") + (r.stderr or ""))
    check("second engine listeners configured", "127.0.0.1:8417" in (r.stdout or "")
          and "127.0.0.1:8418" in (r.stdout or "") and '"tun_enabled": true' in (r.stdout or ""),
          (r.stdout or "").strip())

    if not start_engine_at(data2, "/home/rl/engine2.out", timeout=40):
        check("second engine starts beside peer coverage", False, engine_out_at("/home/rl/engine2.out"))
        stop_secondary_engine(data2)
        return
    check("second engine starts beside peer coverage", True)
    st2 = status_at(data2)
    tun2 = st2.get("tun") or {}
    ev("19-two-engine.txt", "\nsecond status: " + json.dumps(st2, sort_keys=True))
    check("second engine ready", st2.get("ready") is True, json.dumps(st2.get("warnings")))
    check("second engine reports tun_conflict", tun2.get("last_error") == "tun_conflict",
          json.dumps(tun2))
    check("second engine kept its listeners open", st2.get("listeners_open") is True
          and (st2.get("listeners") or {}).get("socks") == "127.0.0.1:8418", json.dumps(st2.get("listeners")))
    check("peer coverage survives second startup", tun_obj().get("state") == "on" and link_present(),
          json.dumps(tun_obj()))
    r = source_probe("203.0.113.7")
    peer = json.loads(r["raw"]).get("peer") if r.get("ok") else None
    check("peer coverage still carries the approved flow", peer == "203.0.113.7", f"peer={peer}")

    r, env = cli_data(data2, ["tun", "on"])
    ev("19-two-engine.txt", "\n$ second tun on\n" + (r.stdout or "") + (r.stderr or "") + f"[exit {r.returncode}]")
    err = (env or {}).get("error") or {}
    check("second tun on refused as conflict", r.returncode == 6 and err.get("kind") == "state_locked"
          and (err.get("detail") or {}).get("reason") == "tun_conflict", f"exit={r.returncode} {err}")
    check("peer coverage survives second tun on", tun_obj().get("state") == "on" and link_present(),
          json.dumps(tun_obj()))

    r, env = cli_data(data2, ["tun", "off"])
    ev("19-two-engine.txt", "\n$ second tun off\n" + (r.stdout or "") + (r.stderr or "") + f"[exit {r.returncode}]")
    err = (env or {}).get("error") or {}
    check("second tun off refused as conflict", r.returncode == 6 and err.get("kind") == "state_locked"
          and (err.get("detail") or {}).get("reason") == "tun_conflict", f"exit={r.returncode} {err}")
    check("peer coverage survives second tun off", tun_obj().get("state") == "on" and link_present(),
          json.dumps(tun_obj()))

    check("second engine stops", stop_secondary_engine(data2))
    check("peer coverage survives second Quit", tun_obj().get("state") == "on" and link_present(),
          json.dumps(tun_obj()))
    r = source_probe("203.0.113.7")
    peer = json.loads(r["raw"]).get("peer") if r.get("ok") else None
    check("peer coverage flow after second Quit", peer == "203.0.113.7", f"peer={peer}")


def engine_out_at(path):
    return ax(f"cat {path} 2>/dev/null || true", timeout=30).stdout or ""


def stop_secondary_engine(data_dir, timeout=20):
    pid = engine_pid_for(data_dir)
    if pid is None:
        return True
    ax(f"kill -TERM {pid}")
    end = time.time() + timeout
    while time.time() < end:
        if engine_pid_for(data_dir) is None:
            return True
        time.sleep(0.05)
    return False


def start_limit_bound():
    section("20-start-limit.txt", "21st start bound and documented reset-failed recovery")
    if tun_obj().get("state") != "on":
        cli_ev("20-start-limit.txt", ["tun", "on"], timeout=90)
        wait_for(lambda: tun_obj().get("state") == "on", 30)
    check("coverage on before limit test", tun_obj().get("state") == "on", json.dumps(tun_obj()))
    # Clear the rate counter so the 21st start is the deterministic bound.
    r = ax("systemctl reset-failed routelatch-tun.socket routelatch-tun.service; "
           "systemctl is-active routelatch-tun.socket")
    ev("20-start-limit.txt", "\n$ reset rate counter before the bounded sequence\n"
       + (r.stdout or "") + (r.stderr or ""))
    before_clear = ax("grep -c 'tun.bypass_clear' /home/rl/engine.out 2>/dev/null || true").stdout.strip()
    attempts = []
    hit_at = None
    for i in range(1, 22):
        rr = ax("systemctl restart routelatch-tun.service", timeout=90)
        active = ax("systemctl is-active routelatch-tun.service 2>/dev/null || true").stdout.strip()
        failed = ax("systemctl is-failed routelatch-tun.service 2>/dev/null || true").stdout.strip()
        attempts.append(f"attempt {i}: restart_exit={rr.returncode} active={active} failed={failed}")
        if failed == "failed":
            hit_at = i
            break
        time.sleep(0.15)
    ev("20-start-limit.txt", "\n$ systemctl restart sequence\n" + "\n".join(attempts))
    check("bounded start limit hit", hit_at is not None and hit_at <= 21, f"hit_at={hit_at}")
    r = ax("systemctl show -p Result -p NRestarts routelatch-tun.service; "
           "journalctl -u routelatch-tun.service -n 8 --no-pager")
    ev("20-start-limit.txt", "\n$ state after limit\n" + (r.stdout or "") + (r.stderr or ""))
    check("start-limit-hit recorded", "start-limit-hit" in (r.stdout or ""), (r.stdout or "").strip())
    check("no rl-tun0 after limit", not link_present())
    v4, v6 = route_count()
    check("no TUN routes after limit", v4 == 0 and v6 == 0, f"v4={v4} v6={v6}")
    t = tun_obj()
    ev("20-start-limit.txt", "\ntun status after limit: " + json.dumps(t, sort_keys=True))
    check("coverage is not reported on after limit", t.get("state") != "on", json.dumps(t))
    check("coverage is not reported as a clean off", t.get("state") == "unavailable", json.dumps(t))
    r, env = cli(["tun", "on"])
    ev("20-start-limit.txt", "\n$ tun on while limit is hit\n" + (r.stdout or "") + (r.stderr or "")
       + f"[exit {r.returncode}]")
    check("tun on fails while the limit is hit", r.returncode != 0 and (env or {}).get("ok") is not True,
          f"exit={r.returncode}")
    after_clear = ax("grep -c 'tun.bypass_clear' /home/rl/engine.out 2>/dev/null || true").stdout.strip()
    check("limit failure did not release the engine bypass", after_clear == before_clear,
          f"{before_clear}->{after_clear}")

    # Documented recovery: reset-failed both units, start the socket, then a
    # later explicit tun on can use the helper again.
    ax("systemctl reset-failed routelatch-tun.socket routelatch-tun.service")
    ax("systemctl start routelatch-tun.socket")
    wait_for(lambda: ax("systemctl is-active routelatch-tun.socket").stdout.strip() == "active", 10)
    r, env = cli_ev("20-start-limit.txt", ["tun", "on"], timeout=90)
    on = wait_for(lambda: tun_obj().get("state") == "on", 30)
    check("reset-failed recovery restores coverage", r.returncode == 0 and bool(on), json.dumps(tun_obj()))
    r = source_probe("203.0.113.7")
    peer = json.loads(r["raw"]).get("peer") if r.get("ok") else None
    check("coverage flow works after documented recovery", peer == "203.0.113.7", f"peer={peer}")


def network_change():
    section("15-r28-network-change.txt", "R-28 default-interface/route change")
    if tun_obj().get("state") != "on":
        cli_ev("15-r28-network-change.txt", ["tun", "on"], timeout=90)
        wait_for(lambda: tun_obj().get("state") == "on", 30)
    r = ax("ip link add dummy0 type dummy && ip link set dummy0 up && "
           "ip addr add 10.255.255.1/32 dev dummy0 && "
           "ip route del default via 172.31.77.20 dev eth0 || true; "
           "ip route replace default via 10.255.255.1 dev dummy0; ip route")
    ev("15-r28-network-change.txt", "\n$ add dummy0/default via dummy0\n" + (r.stdout or "") + (r.stderr or ""))
    check("simulated default-route change applied", r.returncode == 0 and "dummy0" in (r.stdout or ""), (r.stderr or "").strip())
    r = ax("ip route get 203.0.113.8; ip route get 203.0.113.8 oif eth0 2>&1; "
           "ip route get 203.0.113.8 oif dummy0 2>&1; echo ---proc; cat /proc/net/route; "
           "echo ---bypass; grep 'tun.bypass' /home/rl/engine.out | tail -3")
    ev("15-r28-network-change.txt", "\n$ route lookup after change\n" + (r.stdout or "") + (r.stderr or ""))
    # Engine's cached physical interface is still eth0; pool remains reachable,
    # the direct path no longer has a usable route.
    r = source_probe("203.0.113.7", timeout=4)
    peer = json.loads(r["raw"]).get("peer") if r.get("ok") else None
    ev("15-r28-network-change.txt", "\napproved after change: " + json.dumps(r))
    check("stale bypass: approved flow still works", peer == "203.0.113.7", f"peer={peer}")
    r = source_probe("203.0.113.8", timeout=3)
    peer = json.loads(r["raw"]).get("peer") if r.get("ok") else None
    ev("15-r28-network-change.txt", "\ndirect after change: " + json.dumps(r))
    # R-28 documents a stale route until off/on: the old physical interface can
    # still carry the direct path. That is not a leak; what must never happen is
    # a loop or an approved flow turning direct.
    check("stale direct path stays stale or fails closed (no loop)",
          r.get("ok") is False or peer == "172.31.77.10", f"peer={peer}")

    # off/on re-discovers the new default interface (dummy0); nothing silently
    # falls back to direct.
    cli_ev("15-r28-network-change.txt", ["tun", "off"], timeout=90)
    wait_for(lambda: not link_present(), 10)
    cli_ev("15-r28-network-change.txt", ["tun", "on"], timeout=90)
    wait_for(lambda: tun_obj().get("state") == "on", 30)
    r = ax("ip route get 203.0.113.8 oif eth0 2>&1; ip route get 203.0.113.8 oif dummy0 2>&1; "
           "echo ---bypass; grep 'tun.bypass' /home/rl/engine.out | tail -3")
    ev("15-r28-network-change.txt", "\n$ bypass after off/on on dummy0\n" + (r.stdout or "") + (r.stderr or ""))
    r = source_probe("203.0.113.8", timeout=3)
    ev("15-r28-network-change.txt", "\ndirect after off/on on dummy0: " + json.dumps(r))
    check("off/on follows new interface (no silent direct)", r.get("ok") is False, json.dumps(r))
    r = source_probe("203.0.113.7", timeout=4)
    peer = json.loads(r["raw"]).get("peer") if r.get("ok") else None
    ev("15-r28-network-change.txt", "\napproved after off/on on dummy0: " + json.dumps(r))
    # The test pool exit is on-link on eth0, so the kernel can still reach the
    # exit even while the bypass controller is rebound to dummy0. What matters
    # is that approved traffic never turns into a direct destination connection
    # (source 172.31.77.10) and that the direct path fails closed.
    check("off/on rebinds bypass without direct leak",
          r.get("ok") is False or peer == "203.0.113.7", f"peer={peer}")

    # Restore the real test default and recover.
    cli_ev("15-r28-network-change.txt", ["tun", "off"], timeout=90)
    wait_for(lambda: not link_present(), 10)
    ax("ip route replace default via 172.31.77.20 dev eth0; ip link del dummy0")
    cli_ev("15-r28-network-change.txt", ["tun", "on"], timeout=90)
    wait_for(lambda: tun_obj().get("state") == "on", 30)
    r = source_probe("203.0.113.7")
    peer = json.loads(r["raw"]).get("peer") if r.get("ok") else None
    check("restored default recovers pool", peer == "203.0.113.7", f"peer={peer}")
    r = source_probe("203.0.113.8")
    peer = json.loads(r["raw"]).get("peer") if r.get("ok") else None
    check("restored default recovers direct", peer == "172.31.77.10", f"peer={peer}")


def d21_scan():
    section("16-d21-canary.txt", "D21 canary: no flow destination in product surfaces")
    # A flow whose values are not approved rules: canary2-h.invalid + .8.
    t = app_probe(["tcp", "--host", "203.0.113.8", "--port", "7101", "--mode", "tls",
                   "--sni", "canary2-h.invalid", "--timeout", "5"])
    ev("16-d21-canary.txt", "\ncanary flow: " + json.dumps(t))
    check("canary flow executed", t.get("ok") is True, json.dumps(t))
    p = last_peer("tls_request", lambda x: x.get("sni") == "canary2-h.invalid")
    check("peer observed canary SNI", p is not None, json.dumps(p))

    paths = [
        "/home/rl/engine.out",
        ARGS.data + "/log", ARGS.data + "/state.json", ARGS.data + "/config.json",
        ARGS.data + "/backup", ARGS.data + "/run",
        "/run/routelatch-tun", "/etc/routelatch",
        "/etc/systemd/system/routelatch-tun.service", "/etc/systemd/system/routelatch-tun.socket",
    ]
    r = ax("grep -RIn -e canary2-h.invalid -e 203.0.113.8 " +
           " ".join(shlex.quote(s) for s in paths) + " 2>/dev/null || true", timeout=30)
    ev("16-d21-canary.txt", "\n$ grep flow canaries in product surfaces\n" + (r.stdout or ""))
    check("no flow destination in files/log", (r.stdout or "").strip() == "", (r.stdout or "")[:500])
    r = ax("journalctl -u routelatch-tun.service --no-pager 2>/dev/null | "
           "grep -E 'canary2-h\\.invalid|203\\.0\\.113\\.8' || true")
    ev("16-d21-canary.txt", "\n$ journal tun service canary scan\n" + (r.stdout or ""))
    check("no flow destination in TUN journal", (r.stdout or "").strip() == "")
    st = status("16-d21-canary.txt")
    raw = json.dumps(st, sort_keys=True)
    ev("16-d21-canary.txt", "\nstatus scan: " + raw)
    check("no flow destination in status", "canary2-h.invalid" not in raw and "203.0.113.8" not in raw)
    # Approved rules are user data, not flow logs; show that distinction.
    r = ax(f"grep -c 'canary-h.invalid' {ARGS.data}/state.json {ARGS.data}/config.json 2>/dev/null || true")
    ev("16-d21-canary.txt", "\napproved rule presence (expected):\n" + (r.stdout or ""))


def socket_link_witness():
    section("21-socket-link-witness.txt", "missing socket + present rl-tun0 stays fail-closed")
    if tun_obj().get("state") != "on":
        cli_ev("21-socket-link-witness.txt", ["tun", "on"], timeout=90)
        wait_for(lambda: tun_obj().get("state") == "on", 30)
    check("coverage on before witness test", tun_obj().get("state") == "on", json.dumps(tun_obj()))

    # BindsTo: stopping only the socket must stop the activated service, whose
    # shutdown removes the link. That is the documented socket-only recovery.
    r = ax("systemctl stop routelatch-tun.socket", timeout=60)
    ev("21-socket-link-witness.txt", "\n$ systemctl stop routelatch-tun.socket\n"
       + (r.stdout or "") + (r.stderr or ""))
    wait_for(lambda: not link_present(), 10)
    svc = ax("systemctl is-active routelatch-tun.service 2>/dev/null || true").stdout.strip()
    check("socket stop binds the service down", not link_present() and svc != "active", f"service={svc}")
    t = tun_obj()
    ev("21-socket-link-witness.txt", "\ntun status with no socket and no link: " + json.dumps(t, sort_keys=True))
    check("missing socket without a link is not coverage", t.get("state") != "on", json.dumps(t))

    # Witness only: recreate the pinned link with catch-all half routes so a
    # dial that re-entered coverage would blackhole. The contract requires this
    # to stay fail-closed (keep the bypass) and not report off.
    ax("ip link add rl-tun0 type dummy && ip link set rl-tun0 up && "
       "ip route add 0.0.0.0/1 dev rl-tun0 && ip route add 128.0.0.0/1 dev rl-tun0")
    check("witness rl-tun0 present", link_present())
    t = tun_obj()
    ev("21-socket-link-witness.txt", "\ntun status with missing socket + link witness: "
       + json.dumps(t, sort_keys=True))
    check("missing socket + link reports conflict", t.get("state") == "unavailable"
          and t.get("last_error") == "tun_conflict", json.dumps(t))
    r, env = cli(["tun", "off"])
    ev("21-socket-link-witness.txt", "\n$ tun off under socket/link witness\n"
       + (r.stdout or "") + (r.stderr or "") + f"[exit {r.returncode}]")
    t = tun_obj()
    ev("21-socket-link-witness.txt", "\ntun status after witness off: " + json.dumps(t, sort_keys=True))
    check("off does not claim coverage off under the witness",
          r.returncode == 0 and t.get("state") != "off" and t.get("last_error") == "tun_conflict"
          and t.get("desired") is False, f"exit={r.returncode} {json.dumps(t)}")
    check("status says coverage could not be verified off",
          "could not be verified off" in (t.get("detail") or ""), t.get("detail") or "")
    # Direct app traffic that entered the witness coverage must fail closed...
    r = source_probe("203.0.113.7", timeout=3)
    ev("21-socket-link-witness.txt", "\ndirect probe with witness routes: " + json.dumps(r))
    check("witness coverage captures rather than leaks direct", r.get("ok") is False, json.dumps(r))
    # ...while the engine's own dial to the pool exit must still leave through
    # the physical interface. A released bypass would blackhole this flow.
    r = ax("curl -sS --max-time 6 --insecure --socks5-hostname 127.0.0.1:8318 "
           "https://canary-h.invalid:7101/ 2>&1", user="1500:1500", timeout=20)
    ev("21-socket-link-witness.txt", "\n$ curl via SOCKS with witness routes\n"
       + (r.stdout or "") + (r.stderr or "") + f"[exit {r.returncode}]")
    e = last_exit(lambda x: x.get("event") == "connect" and x.get("port") == 7101)
    check("witness off kept the engine bypass (proxy flow reaches pool)",
          r.returncode == 0 and ("200 OK" in (r.stdout or "") or "TLS-OK" in (r.stdout or ""))
          and e is not None and e.get("target") == "canary-h.invalid",
          f"exit={r.returncode} {(r.stdout or '')[:120]}")

    # Remove the witness. Now the same missing socket proves off and clears the
    # bypass, then the helper is restored and coverage can be requested again.
    ax("ip link del rl-tun0 2>/dev/null || true")
    r, env = cli(["tun", "off"])
    ev("21-socket-link-witness.txt", "\n$ tun off after witness removal\n"
       + (r.stdout or "") + (r.stderr or "") + f"[exit {r.returncode}]")
    t = tun_obj()
    check("off after witness removal is a clean off", r.returncode == 0
          and t.get("desired") is False and t.get("state") == "off", json.dumps(t))
    ax("systemctl reset-failed routelatch-tun.socket routelatch-tun.service 2>/dev/null || true; "
       "systemctl start routelatch-tun.socket 2>/dev/null || true")
    wait_for(lambda: ax("systemctl is-active routelatch-tun.socket").stdout.strip() == "active", 10)
    r, env = cli_ev("21-socket-link-witness.txt", ["tun", "on"], timeout=90)
    on = wait_for(lambda: tun_obj().get("state") == "on", 30)
    check("coverage returns after helper restart", r.returncode == 0 and bool(on), json.dumps(tun_obj()))
    r = source_probe("203.0.113.7")
    peer = json.loads(r["raw"]).get("peer") if r.get("ok") else None
    check("coverage flow after witness recovery", peer == "203.0.113.7", f"peer={peer}")


def uninstall_and_helper_missing():
    section("17-uninstall-helper.txt", "tun uninstall preservation and helper-missing warning")
    cli_ev("17-uninstall-helper.txt", ["tun", "off"], timeout=90)
    wait_for(lambda: not link_present(), 10)
    r = ax(f"SUDO_UID=1500 {shlex.quote(ARGS.binary)} --json tun uninstall", timeout=90)
    env = json_env(r)
    ev("17-uninstall-helper.txt", "\n$ tun uninstall\n" + (r.stdout or "") + (r.stderr or "") + f"[exit {r.returncode}]")
    check("tun uninstall exit 0", r.returncode == 0 and (env or {}).get("ok") is True, f"exit={r.returncode}")
    r = ax("for f in /etc/systemd/system/routelatch-tun.service /etc/systemd/system/routelatch-tun.socket "
           "/etc/routelatch/tun.json /etc/tmpfiles.d/routelatch-tun.conf "
           "/usr/libexec/routelatch/tun-service /run/routelatch-tun/control.sock /run/routelatch-tun/lease.json; "
           "do if [ -e \"$f\" ]; then echo present:$f; else echo removed:$f; fi; done; "
           f"ls -l {ARGS.data}/config.json {ARGS.data}/state.json 2>&1")
    ev("17-uninstall-helper.txt", "\n$ uninstall layout\n" + (r.stdout or "") + (r.stderr or ""))
    check("uninstall removes owned files", "present:" not in (r.stdout or ""), (r.stdout or "")[:500])
    check("uninstall preserves user data", f"{ARGS.data}/config.json" in (r.stdout or "")
          and f"{ARGS.data}/state.json" in (r.stdout or ""))
    t = tun_obj("17-uninstall-helper.txt")
    check("helper reports missing", t.get("helper") == "missing" and t.get("state") == "off", json.dumps(t))

    r, env = cli_ev("17-uninstall-helper.txt", ["tun", "on"])
    err = (env or {}).get("error") or {}
    check("tun on without helper refused", r.returncode == 7 and err.get("kind") == "unsupported"
          and (err.get("detail") or {}).get("reason") == "tun_helper_unavailable", f"exit={r.returncode} {err}")
    cfg = status("17-uninstall-helper.txt").get("tun") or {}
    check("failed enable keeps the desire", cfg.get("desired") is True, json.dumps(cfg))

    # Restart the engine with the saved desire and no helper: warning, normal proxy.
    if not stop_engine("TERM"):
        check("engine stop before helper-missing restart", False)
    if not start_engine():
        check("engine restart without helper", False)
        return
    check("engine ready without helper", wait_engine_ready(20))
    st = status("17-uninstall-helper.txt")
    t = st.get("tun") or {}
    ev("17-uninstall-helper.txt", "\nstatus without helper: " + json.dumps(st, sort_keys=True))
    check("missing helper is visible", t.get("helper") == "missing" and t.get("state") == "unavailable",
          json.dumps(t))
    # contracts §10.2: a failed runtime status is reported as
    # tun_service_unavailable (not tun_helper_missing), because the absence of
    # an answer cannot prove the helper is missing.
    warns = st.get("warnings") or []
    check("missing helper warning is surfaced", "tun_service_unavailable" in warns,
          json.dumps(warns))
    r = ax("ss -lnt | grep -E ':(8317|8318) '")
    ev("17-uninstall-helper.txt", "\nlisteners: " + (r.stdout or ""))
    check("normal listeners stay available", r.returncode == 0 and ":8317 " in (r.stdout or "") and ":8318 " in (r.stdout or ""))
    cli_ev("17-uninstall-helper.txt", ["tun", "off"], timeout=90)
    t = tun_obj()
    check("tun off clears desire without helper", t.get("desired") is False and t.get("state") == "off", json.dumps(t))
    check("engine still ready after helper-missing path", status("").get("ready") is True)


def final_cleanup():
    section("18-final-cleanup.txt", "final no-orphan checks")
    stop_engine("TERM")
    wait_for(lambda: not link_present(), 10)
    r = ax("ip link show rl-tun0 2>&1 || true; echo ---; ip route | grep -E '198\\.18\\.|^0\\.0\\.0\\.0/1|^128\\.0\\.0\\.0/1' || true; "
           "echo ---; ip -6 route | grep -E 'fc00::/64|^::/1|^8000::/1' || true; echo ---; "
           "pgrep -u 1500 -a -f 'run --headless' || true; echo ---; ls -la /run/routelatch-tun 2>&1 || true")
    ev("18-final-cleanup.txt", "\n" + (r.stdout or ""))
    check("no TUN link at end", ax("ip link show rl-tun0").returncode != 0, (r.stdout or "").splitlines()[0] if (r.stdout or "") else "")
    v4, v6 = route_count()
    check("no TUN routes at end", v4 == 0 and v6 == 0, f"v4={v4} v6={v6}")
    check("no engine process at end", engine_pid() is None)
    check("no helper socket/lease at end",
          not ax("test -e /run/routelatch-tun/control.sock -o -e /run/routelatch-tun/lease.json").returncode == 0)
    check("no helper unit at end", not ax("test -e /etc/systemd/system/routelatch-tun.service").returncode == 0)


def finish():
    passed = sum(1 for s, _, _ in RESULTS if s == "PASS")
    failed = sum(1 for s, _, _ in RESULTS if s == "FAIL")
    skipped = sum(1 for s, _, _ in RESULTS if s == "NOTRUN")
    summary = f"SUMMARY passed={passed} failed={failed} notrun={skipped} total={len(RESULTS)}"
    print(summary)
    ev("results.txt", "\n" + summary)
    ev("run.log", f"\nSUMMARY passed={passed} failed={failed} notrun={skipped}")
    return 0 if failed == 0 else 1


if __name__ == "__main__":
    sys.exit(main())
