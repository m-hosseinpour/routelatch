#!/usr/bin/env python3
"""Disposable R16-A peer fixture: TLS/HTTP/echo TCP servers and UDP sinks.

Runs in the peer container, outside the TUN namespace. Every accepted TCP
connection records the destination port and, for TLS/HTTP, the visible SNI or
Host value; every received UDP datagram is logged. This is the observation
point for DNS-direct, UDP-blackhole and the engine-egress loop count.
"""
import argparse
import json
import socket
import ssl
import threading
import time

LOCK = threading.Lock()


def log(path, obj):
    with LOCK:
        with open(path, "a", encoding="utf-8") as f:
            f.write(json.dumps(obj, sort_keys=True) + "\n")


def serve_tcp(host, port, family, handler, log_path, tag):
    s = socket.socket(family, socket.SOCK_STREAM)
    s.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEADDR, 1)
    if family == socket.AF_INET6:
        s.setsockopt(socket.IPPROTO_IPV6, socket.IPV6_V6ONLY, 1)
    s.bind((host, port))
    s.listen(64)
    log(log_path, {"event": "listen", "port": port, "tag": tag, "family": family.name})
    while True:
        conn, addr = s.accept()
        threading.Thread(target=handler, args=(conn, addr, log_path, tag), daemon=True).start()


def tls_handler(ctx, conn, addr, log_path, tag):
    sni = {"name": None}

    def cb(sslobj, server_name, _ctx):
        sni["name"] = server_name
        try:
            sslobj._rl_sni = server_name
        except AttributeError:
            pass

    ctx.set_servername_callback(cb)
    try:
        tls = ctx.wrap_socket(conn, server_side=True)
    except (ssl.SSLError, OSError) as e:
        log(log_path, {"event": "tls_error", "port": tag, "peer": addr[0], "error": str(e)})
        conn.close()
        return
    try:
        req = tls.recv(4096)
        log(log_path, {"event": "tls_request", "port": tag, "peer": f"{addr[0]}:{addr[1]}",
                       "sni": getattr(tls, "_rl_sni", sni["name"])})
        tls.sendall(b"HTTP/1.1 200 OK\r\nContent-Length: 6\r\nConnection: close\r\n\r\nTLS-OK")
    except OSError as e:
        log(log_path, {"event": "tls_read_error", "port": tag, "error": str(e)})
    finally:
        tls.close()


def echo_handler(conn, addr, log_path, tag):
    try:
        data = conn.recv(4096)
        log(log_path, {"event": "echo", "port": tag, "peer": f"{addr[0]}:{addr[1]}", "bytes": len(data)})
        conn.sendall(b"ECHO:" + data)
    except OSError as e:
        log(log_path, {"event": "echo_error", "port": tag, "error": str(e)})
    finally:
        conn.close()


def http_handler(conn, addr, log_path, tag):
    try:
        data = b""
        while b"\r\n\r\n" not in data and len(data) < 8192:
            chunk = conn.recv(4096)
            if not chunk:
                break
            data += chunk
        host = ""
        for line in data.split(b"\r\n"):
            if line.lower().startswith(b"host:"):
                host = line.split(b":", 1)[1].strip().decode("ascii", "replace")
        log(log_path, {"event": "http", "port": tag, "peer": f"{addr[0]}:{addr[1]}", "host": host})
        conn.sendall(b"HTTP/1.1 200 OK\r\nContent-Length: 5\r\nConnection: close\r\n\r\nHTTP-OK")
    except OSError as e:
        log(log_path, {"event": "http_error", "port": tag, "error": str(e)})
    finally:
        conn.close()


def udp_sink(host, port, family, log_path, tag):
    s = socket.socket(family, socket.SOCK_DGRAM)
    if family == socket.AF_INET6:
        s.setsockopt(socket.IPPROTO_IPV6, socket.IPV6_V6ONLY, 1)
    s.bind((host, port))
    log(log_path, {"event": "udp_listen", "port": port, "tag": tag, "family": family.name})
    while True:
        data, addr = s.recvfrom(65535)
        log(log_path, {"event": "udp_datagram", "port": port, "tag": tag,
                       "peer": f"{addr[0]}:{addr[1]}", "bytes": len(data)})


def main():
    p = argparse.ArgumentParser()
    p.add_argument("--cert", default="/opt/fixture/cert.pem")
    p.add_argument("--key", default="/opt/fixture/key.pem")
    p.add_argument("--log", default="/opt/fixture/peer.log")
    p.add_argument("--udp-log", default="/opt/fixture/udp.log")
    args = p.parse_args()

    ctx4 = ssl.SSLContext(ssl.PROTOCOL_TLS_SERVER)
    ctx4.load_cert_chain(args.cert, args.key)
    ctx6 = ssl.SSLContext(ssl.PROTOCOL_TLS_SERVER)
    ctx6.load_cert_chain(args.cert, args.key)

    threading.Thread(target=serve_tcp, args=("0.0.0.0", 7101, socket.AF_INET,
                     lambda c, a, l, t: tls_handler(ctx4, c, a, l, t), args.log, "tls4"), daemon=True).start()
    threading.Thread(target=serve_tcp, args=("::", 7101, socket.AF_INET6,
                     lambda c, a, l, t: tls_handler(ctx6, c, a, l, t), args.log, "tls6"), daemon=True).start()
    threading.Thread(target=serve_tcp, args=("0.0.0.0", 7102, socket.AF_INET, echo_handler, args.log, "echo4"), daemon=True).start()
    threading.Thread(target=serve_tcp, args=("::", 7102, socket.AF_INET6, echo_handler, args.log, "echo6"), daemon=True).start()
    threading.Thread(target=serve_tcp, args=("0.0.0.0", 7103, socket.AF_INET, echo_handler, args.log, "pool4"), daemon=True).start()
    threading.Thread(target=serve_tcp, args=("::", 7103, socket.AF_INET6, echo_handler, args.log, "pool6"), daemon=True).start()
    threading.Thread(target=serve_tcp, args=("0.0.0.0", 7104, socket.AF_INET, http_handler, args.log, "http4"), daemon=True).start()
    threading.Thread(target=serve_tcp, args=("::", 7104, socket.AF_INET6, http_handler, args.log, "http6"), daemon=True).start()
    for host, port, fam, tag in [("0.0.0.0", 9999, socket.AF_INET, "udp4"), ("::", 9999, socket.AF_INET6, "udp6"),
                                 ("0.0.0.0", 443, socket.AF_INET, "quic4"), ("::", 443, socket.AF_INET6, "quic6")]:
        threading.Thread(target=udp_sink, args=(host, port, fam, args.udp_log, tag), daemon=True).start()
    log(args.log, {"event": "ready", "ts": time.strftime("%Y-%m-%dT%H:%M:%SZ", time.gmtime())})
    threading.Event().wait()


if __name__ == "__main__":
    main()
