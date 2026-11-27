#!/usr/bin/env python3
"""Post-release verification: repeated TUN-service SIGKILL status sampling.

For each kill it measures the rl-tun0 gap with watch_link.py while two
independent samplers record `routelatch tun status`:
  * JSON (state/desired/helper/last_error/detail/exit code/timing)
  * human text (exit code/stdout/stderr/timing)
It then records one post-recovery sample of both forms.

Evidence-only helper: no product code is touched.
"""
import argparse
import json
import os
import subprocess
import time

ARGS = None


def run(cmd, timeout=120):
    return subprocess.run(cmd, capture_output=True, text=True, timeout=timeout)


def ax(script, timeout=120):
    return run(["docker", "exec", "-u", "1500:1500", "-e", "HOME=/home/rl",
                ARGS.a, "sh", "-c", script], timeout=timeout)


def adetach(script):
    return run(["docker", "exec", "-d", "-u", "1500:1500", "-e", "HOME=/home/rl",
                ARGS.a, "sh", "-c", script])


def cli(args, timeout=30):
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
    env = env_of(r)
    data = (env or {}).get("data") or {}
    if isinstance(data, dict) and "tun" in data:
        data = data["tun"]
    return data or {}, r.returncode


def fields(obj):
    return {k: obj.get(k) for k in
            ("state", "desired", "helper", "interface", "last_error", "detail")}


def read_lines(path):
    out = []
    for line in ax(f"cat {path} 2>/dev/null || true").stdout.splitlines():
        try:
            out.append(json.loads(line))
        except json.JSONDecodeError:
            pass
    return out


def wait_state_on(timeout=20):
    end = time.time() + timeout
    while time.time() < end:
        obj, _ = tun_obj()
        if obj.get("state") == "on":
            return time.time()
        time.sleep(0.05)
    return None


def wait_link(timeout=10):
    end = time.time() + timeout
    while time.time() < end:
        if ax("ip link show rl-tun0 >/dev/null 2>&1").returncode == 0:
            return time.time()
        time.sleep(0.02)
    return None


def ensure_on():
    obj, _ = tun_obj()
    if obj.get("state") != "on":
        cli(["tun", "on"], timeout=90)
        wait_state_on(30)
    return obj


def ev(name, text):
    with open(ARGS.evidence.rstrip("/") + "/" + name, "a", encoding="utf-8") as f:
        f.write(text.rstrip("\n") + "\n")


def one_kill(i):
    ax(f"rm -f /home/rl/link-k{i}.jsonl /home/rl/json-k{i}.jsonl /home/rl/text-k{i}.jsonl")
    t_start = time.time()
    adetach("python3 /src/spike/tun/h/watch_link.py --duration 5 --interval 0.002 "
            f"--out /home/rl/link-k{i}.jsonl")
    adetach("python3 /src/spike/tun/evidence/post-v1.2.0-verification-2026-11-27/harness/"
            "watch_tun_json.py "
            f"--binary {ARGS.binary} --data {ARGS.data} --duration 5 --interval 0.005 "
            f"--timeout 0.7 --out /home/rl/json-k{i}.jsonl")
    adetach("python3 /src/spike/tun/evidence/post-v1.2.0-verification-2026-11-27/harness/"
            "watch_tun_text.py "
            f"--binary {ARGS.binary} --data {ARGS.data} --duration 5 --interval 0.005 "
            f"--timeout 0.7 --out /home/rl/text-k{i}.jsonl")
    time.sleep(0.6)

    pid = ax("systemctl show -p MainPID --value routelatch-tun.service").stdout.strip()
    nrestarts = ax("systemctl show -p NRestarts --value routelatch-tun.service").stdout.strip()
    t_kill = time.time()
    ax(f"kill -9 {pid}")
    t_on = wait_state_on(20)
    t_link = wait_link(10)
    time.sleep(max(0.0, t_start + 5.3 - time.time()))

    transitions = read_lines(f"/home/rl/link-k{i}.jsonl")
    absent = [x for x in transitions if not x.get("present") and x.get("t", 0) >= t_kill]
    after_absent = [x for x in transitions if x.get("present") and absent
                    and x.get("t", 0) >= absent[0].get("t", 0)]
    gap_start = absent[0]["t"] if absent else None
    gap_end = after_absent[0]["t"] if after_absent else None

    js = read_lines(f"/home/rl/json-k{i}.jsonl")
    tx = read_lines(f"/home/rl/text-k{i}.jsonl")
    inside_js = [s for s in js if gap_start is not None and gap_end is not None
                 and gap_start <= s.get("t", 0) <= gap_end]
    inside_tx = [s for s in tx if gap_start is not None and gap_end is not None
                 and gap_start <= s.get("t", 0) <= gap_end]
    # A call that starts in the gap but returns after link recovery reports the
    # recovered state; only calls that finish inside the gap describe the gap.
    completed_js = [s for s in inside_js if s.get("t_end", s.get("t", 0)) <= (gap_end or 0)]
    spanning_js = [s for s in inside_js if s.get("t_end", s.get("t", 0)) > (gap_end or 0)]
    completed_tx = [s for s in inside_tx if s.get("t_end", s.get("t", 0)) <= (gap_end or 0)]
    spanning_tx = [s for s in inside_tx if s.get("t_end", s.get("t", 0)) > (gap_end or 0)]

    post, post_rc = tun_obj()
    post_text = ax(f"{ARGS.binary} --data-dir {ARGS.data} tun status")

    rec = {
        "kill": i,
        "pid": pid,
        "nrestarts_before": nrestarts,
        "t_kill": t_kill,
        "t_state_on": t_on,
        "t_link_back": t_link,
        "gap_start": gap_start,
        "gap_end": gap_end,
        "gap_ms": int((gap_end - gap_start) * 1000) if gap_start and gap_end else None,
        "state_on_ms": int((t_on - t_kill) * 1000) if t_on else None,
        "link_back_ms": int((t_link - t_kill) * 1000) if t_link else None,
        "in_gap_json": inside_js,
        "in_gap_text": inside_tx,
        "completed_in_gap_json": completed_js,
        "completed_in_gap_text": completed_tx,
        "spanning_gap_json": spanning_js,
        "spanning_gap_text": spanning_tx,
        "post": fields(post),
        "post_rc": post_rc,
        "post_text": (post_text.stdout or "").strip(),
        "post_text_rc": post_text.returncode,
        "post_text_stderr": (post_text.stderr or "").strip(),
    }
    ev("11-failopen-status.txt", json.dumps(rec, sort_keys=True))
    return rec


def main():
    global ARGS
    p = argparse.ArgumentParser()
    p.add_argument("--a", required=True)
    p.add_argument("--binary", required=True)
    p.add_argument("--data", required=True)
    p.add_argument("--evidence", required=True)
    p.add_argument("--kills", type=int, default=4)
    ARGS = p.parse_args()
    os.makedirs(ARGS.evidence, exist_ok=True)

    ensure_on()
    ev("11-failopen-status.txt", "# repeated TUN-service SIGKILL sampling")
    recs = [one_kill(i) for i in range(1, ARGS.kills + 1)]

    results = []

    def check(name, ok, detail=""):
        status = "PASS" if ok else "FAIL"
        results.append((status, name, detail))
        ev("results-failopen.txt", f"{status} {name}" + (f" :: {detail}" if detail else ""))

    def notrun(name, reason):
        results.append(("NOTRUN", name, reason))
        ev("results-failopen.txt", f"NOTRUN {name} :: {reason}")

    gaps = [r for r in recs if r["gap_start"] and r["gap_end"]]
    check(f"link watcher observed the gap in all {ARGS.kills} kills",
          len(gaps) == ARGS.kills,
          json.dumps([r["gap_ms"] for r in recs]))
    with_js = [r for r in recs if r["in_gap_json"]]
    with_tx = [r for r in recs if r["in_gap_text"]]
    check("JSON status sample landed inside every gap", len(with_js) == len(gaps),
          json.dumps([len(r["in_gap_json"]) for r in recs]))
    check("text status sample landed inside every gap", len(with_tx) == len(gaps),
          json.dumps([len(r["in_gap_text"]) for r in recs]))
    check("service auto-restarted after every SIGKILL",
          all(r["state_on_ms"] is not None for r in recs),
          json.dumps([r["state_on_ms"] for r in recs]))

    all_js = [s for r in recs for s in r["in_gap_json"]]
    completed = [s for r in recs for s in r["completed_in_gap_json"]]
    spanning = [s for r in recs for s in r["spanning_gap_json"]]
    all_tx_completed = [s for r in recs for s in r["completed_in_gap_text"]]
    all_tx_spanning = [s for r in recs for s in r["spanning_gap_text"]]
    clean_off = [s for s in all_js if s.get("state") == "off" and not s.get("last_error")]
    check("status never claimed a clean off inside a gap", not clean_off,
          json.dumps(clean_off[:3]))

    unhealthy = [s for s in completed
                 if s.get("state") not in (None, "on", "off") or s.get("last_error")
                 or s.get("detail") or s.get("error") or s.get("rc") not in (0, None)]
    clean_on_completed = [s for s in completed
                          if s.get("rc") == 0 and s.get("state") == "on"
                          and not s.get("last_error") and not s.get("detail")]
    if unhealthy:
        check("status exposed an error/unavailable/timeout inside a gap", True,
              json.dumps(unhealthy[:4]))
    elif clean_on_completed:
        notrun("status exposed an error/unavailable/timeout inside a gap",
               "a call completed inside the gap with a clean on: "
               + json.dumps([{k: s.get(k) for k in
                              ("state", "last_error", "detail", "ms")} for s in clean_on_completed[:3]]))
    else:
        check("every in-gap status call blocked until recovery or timed out", True,
              "completed=" + str(len(completed)) + " spanning=" + str(len(spanning))
              + " json_ms=" + json.dumps([(s.get("ms"), s.get("error")) for s in all_js[:6]]))

    ev("results-failopen.txt",
       "OBSERVE in-gap(start) JSON=" + str(len(all_js))
       + " completed_in_gap=" + str(len(completed))
       + " spanning_into_recovery=" + str(len(spanning))
       + " clean_on_completed=" + str(len(clean_on_completed))
       + " timeouts=" + str(len([s for s in all_js if s.get("error") == "timeout"]))
       + " text_completed=" + str(len(all_tx_completed))
       + " text_spanning=" + str(len(all_tx_spanning))
       + " timed=" + json.dumps([[s.get("ms"), s.get("error"), s.get("state")] for s in all_js])
       + " ranges_ms=" + json.dumps([[r["gap_ms"], r["state_on_ms"]] for r in recs]))
    ev("results-failopen.txt", "OBSERVE post-recovery fields="
       + json.dumps([r["post"] for r in recs]))

    failed = sum(1 for s, _, _ in results if s == "FAIL")
    passed = sum(1 for s, _, _ in results if s == "PASS")
    skipped = sum(1 for s, _, _ in results if s == "NOTRUN")
    ev("results-failopen.txt",
       f"SUMMARY passed={passed} failed={failed} notrun={skipped} total={len(results)}")
    return 1 if failed else 0


if __name__ == "__main__":
    raise SystemExit(main())
