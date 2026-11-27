import socket, sys, json, time

peer = sys.argv[1]
port = int(sys.argv[2])
label = sys.argv[3]
s = socket.socket(socket.AF_INET, socket.SOCK_DGRAM)
s.settimeout(3.0)
payload = label.encode()
t0 = time.monotonic()
try:
    s.sendto(payload, (peer, port))
    data, _ = s.recvfrom(65535)
    rtt = (time.monotonic() - t0) * 1000.0
    print(json.dumps({"label": label, "ok": data == payload, "rtt_ms": round(rtt, 1)}), flush=True)
except Exception as e:
    print(json.dumps({"label": label, "ok": False, "error": type(e).__name__}), flush=True)
