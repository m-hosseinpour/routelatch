import json, os, signal, socket, subprocess, sys, time
sys.path.insert(0, "/w")
import socks_udp

OUT = "/w/out"
PROCS = []

def start(name, argv, wait_port=None):
    log = open(f"{OUT}/{name}.log", "wb")
    p = subprocess.Popen(argv, stdout=log, stderr=subprocess.STDOUT)
    PROCS.append((name, p, log))
    if wait_port:
        host, port = wait_port
        for _ in range(100):
            try:
                s = socket.create_connection((host, port), timeout=0.2)
                s.close()
                return p
            except OSError:
                time.sleep(0.05)
        raise RuntimeError(f"{name} did not open {wait_port}")
    return p

DEST = ("93.184.216.34", 9000)

def result(name, fn, dest=DEST, payload=b"hello-r16j"):
    try:
        r = fn(dest, payload)
        r["test"] = name
        print(json.dumps(r), flush=True)
        return r
    except Exception as e:
        print(json.dumps({"test": name, "ok": False, "error": type(e).__name__ + ": " + str(e)}), flush=True)
        return {"test": name, "ok": False}

def via(proxy):
    def f(dest, payload):
        t, u, relay = socks_udp.associate(*proxy)
        try:
            body, rtt = socks_udp.send_recv(u, relay, dest, payload)
            return {"ok": body == payload, "len": len(body), "rtt_ms": round(rtt, 2)}
        finally:
            u.close(); t.close()
    return f

def main():
    os.makedirs(OUT, exist_ok=True)
    subprocess.run(["ip", "addr", "add", "93.184.216.34/32", "dev", "lo"], check=True)
    start("echo", ["python3", "/w/udp_echo.py", "9000", "93.184.216.34"])
    time.sleep(0.4)

    start("singbox-direct", ["/w/bin/sing-box", "run", "-c", "/w/conf/singbox-mixed-direct.json"], ("127.0.0.1", 10003))
    start("xray-vless-server", ["/w/bin/xray", "run", "-c", "/w/conf/xray-vless-server.json"], ("127.0.0.1", 10001))
    start("xray-vless-client", ["/w/bin/xray", "run", "-c", "/w/conf/xray-vless-client.json"], ("127.0.0.1", 10002))
    start("xray-socks-mixed", ["/w/bin/xray", "run", "-c", "/w/conf/xray-socks-to-mixed.json"], ("127.0.0.1", 10006))

    # Trojan: plaintext first; the fixture does not need the product TLS policy.
    start("xray-trojan-server", ["/w/bin/xray", "run", "-c", "/w/conf/xray-trojan-server.json"], ("127.0.0.1", 10007))
    start("xray-trojan-client", ["/w/bin/xray", "run", "-c", "/w/conf/xray-trojan-client.json"], ("127.0.0.1", 10008))

    # hysteria2 (QUIC, self-signed local certificate).
    start("singbox-hy2-server", ["/w/bin/sing-box", "run", "-c", "/w/conf/singbox-hy2-server.json"])
    time.sleep(0.8)
    start("singbox-hy2-client", ["/w/bin/sing-box", "run", "-c", "/w/conf/singbox-hy2-client.json"], ("127.0.0.1", 10005))

    tests = {
        "socks5_exit_udp": via(("127.0.0.1", 10003)),
        "xray_socks_outbound_udp": via(("127.0.0.1", 10006)),
        "vless_udp": via(("127.0.0.1", 10002)),
        "trojan_udp": via(("127.0.0.1", 10008)),
        "hysteria2_udp": via(("127.0.0.1", 10005)),
    }
    for name, fn in tests.items():
        result(name, fn)

    # Datagram size behavior for the two tunnel styles.
    for name, proxy in (("vless", ("127.0.0.1", 10002)), ("hysteria2", ("127.0.0.1", 10005))):
        for size in (1200, 1400, 8192, 60000):
            payload = bytes([65 + (size % 20)]) * size
            result(f"{name}_size_{size}", via(proxy), payload=payload)

    # Local loss/delay comparison (loopback netem; controlled, not a public exit).
    subprocess.run(["tc", "qdisc", "add", "dev", "lo", "root", "netem", "delay", "20ms", "loss", "1%"], check=False)
    try:
        for name, proxy in (("vless_netem", ("127.0.0.1", 10002)),
                            ("hysteria2_netem", ("127.0.0.1", 10005)),
                            ("socks5_netem", ("127.0.0.1", 10003))):
            rtts, ok = [], 0
            for i in range(50):
                try:
                    t, u, relay = socks_udp.associate(*proxy)
                    try:
                        body, rtt = socks_udp.send_recv(u, relay, DEST, f"p{i}".encode(), timeout=2.0)
                        if body == f"p{i}".encode():
                            ok += 1
                            rtts.append(rtt)
                    finally:
                        u.close(); t.close()
                except Exception:
                    pass
            rtts.sort()
            print(json.dumps({"test": name, "ok": ok == 100, "sent": 50, "received": ok,
                              "p50_ms": round(rtts[len(rtts)//2], 1) if rtts else None,
                              "p95_ms": round(rtts[int(len(rtts)*0.95)], 1) if rtts else None,
                              "max_ms": round(max(rtts), 1) if rtts else None}), flush=True)
    finally:
        subprocess.run(["tc", "qdisc", "del", "dev", "lo", "root"], check=False)

if __name__ == "__main__":
    try:
        main()
    finally:
        for name, p, log in PROCS:
            p.terminate()
        time.sleep(0.3)
        for name, p, log in PROCS:
            if p.poll() is None:
                p.kill()
            log.close()
