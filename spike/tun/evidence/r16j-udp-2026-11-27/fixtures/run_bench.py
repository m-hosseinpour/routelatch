import json, os, socket, subprocess, sys, time
sys.path.insert(0, "/w")
import socks_udp

OUT = "/w/out"
PROCS = []
DEST = ("93.184.216.34", 9000)

def start(name, argv, wait_port=None):
    log = open(f"{OUT}/{name}-bench.log", "wb")
    p = subprocess.Popen(argv, stdout=log, stderr=subprocess.STDOUT)
    PROCS.append((name, p, log))
    if wait_port:
        for _ in range(100):
            try:
                s = socket.create_connection(wait_port, timeout=0.2); s.close(); return p
            except OSError:
                time.sleep(0.05)
        raise RuntimeError(name + " did not start")

def bench(name, proxy):
    rtts, ok = [], 0
    t, u, relay = socks_udp.associate(*proxy)
    try:
        for i in range(200):
            try:
                body, rtt = socks_udp.send_recv(u, relay, DEST, b"seq%05d" % i, timeout=2.0)
                if body == b"seq%05d" % i:
                    ok += 1; rtts.append(rtt)
            except Exception:
                pass
        # Burst: 200 datagrams back to back, then collect replies.
        for i in range(200):
            pkt = b"\x00\x00\x00\x01" + socket.inet_aton(DEST[0]) + (9000).to_bytes(2, "big") + b"bst%05d" % i
            u.sendto(pkt, relay)
        got = set(); deadline = time.monotonic() + 3.0
        while time.monotonic() < deadline:
            try:
                data, _ = u.recvfrom(65535)
            except socket.timeout:
                break
            # payload starts after 10-byte IPv4 UDP header
            if len(data) >= 13 and data[10:13] == b"bst":
                got.add(int(data[13:18]))
    finally:
        u.close(); t.close()
    rtts.sort()
    print(json.dumps({"test": name, "sequential_ok": ok, "sequential_sent": 200,
                      "p50_ms": round(rtts[len(rtts)//2], 1) if rtts else None,
                      "p95_ms": round(rtts[int(len(rtts)*.95)], 1) if rtts else None,
                      "burst_unique_replies": len(got), "burst_sent": 200}), flush=True)

def main():
    os.makedirs(OUT, exist_ok=True)
    subprocess.run(["ip", "addr", "add", "93.184.216.34/32", "dev", "lo"], check=True)
    start("echo", ["python3", "/w/udp_echo.py", "9000", "93.184.216.34"])
    time.sleep(0.3)
    start("singbox-direct", ["/w/bin/sing-box", "run", "-c", "/w/conf/singbox-mixed-direct.json"], ("127.0.0.1", 10003))
    start("xray-vless-server", ["/w/bin/xray", "run", "-c", "/w/conf/xray-vless-server.json"], ("127.0.0.1", 10001))
    start("xray-vless-client", ["/w/bin/xray", "run", "-c", "/w/conf/xray-vless-client.json"], ("127.0.0.1", 10002))
    start("singbox-hy2-server", ["/w/bin/sing-box", "run", "-c", "/w/conf/singbox-hy2-server.json"])
    time.sleep(0.8)
    start("singbox-hy2-client", ["/w/bin/sing-box", "run", "-c", "/w/conf/singbox-hy2-client.json"], ("127.0.0.1", 10005))
    time.sleep(0.3)

    print(json.dumps({"phase": "clean-loopback"}), flush=True)
    bench("vless_clean", ("127.0.0.1", 10002))
    bench("hysteria2_clean", ("127.0.0.1", 10005))
    bench("socks5_clean", ("127.0.0.1", 10003))

    subprocess.run(["tc", "qdisc", "add", "dev", "lo", "root", "netem", "delay", "20ms", "loss", "1%"], check=False)
    try:
        print(json.dumps({"phase": "netem-20ms-1pct"}), flush=True)
        bench("vless_netem", ("127.0.0.1", 10002))
        bench("hysteria2_netem", ("127.0.0.1", 10005))
        bench("socks5_netem", ("127.0.0.1", 10003))
    finally:
        subprocess.run(["tc", "qdisc", "del", "dev", "lo", "root"], check=False)

if __name__ == "__main__":
    try:
        main()
    finally:
        for _, p, log in PROCS:
            p.terminate()
        time.sleep(0.2)
        for _, p, log in PROCS:
            if p.poll() is None: p.kill()
            log.close()
