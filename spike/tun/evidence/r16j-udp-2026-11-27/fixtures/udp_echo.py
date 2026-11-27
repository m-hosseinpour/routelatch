import socket, sys, json

port = int(sys.argv[1])
bind = sys.argv[2] if len(sys.argv) > 2 else "127.0.0.1"
s = socket.socket(socket.AF_INET, socket.SOCK_DGRAM)
s.bind((bind, port))
print(json.dumps({"event": "listening", "addr": bind, "port": port}), flush=True)
while True:
    data, addr = s.recvfrom(65535)
    s.sendto(data, addr)
