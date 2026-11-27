import socket, sys, json

port = int(sys.argv[1])
s = socket.socket(socket.AF_INET, socket.SOCK_DGRAM)
s.bind(("0.0.0.0", port))
print(json.dumps({"event": "listening", "port": port}), flush=True)
n = 0
while True:
    data, addr = s.recvfrom(65535)
    n += 1
    print(json.dumps({"event": "datagram", "n": n, "len": len(data)}), flush=True)
    s.sendto(data, addr)
