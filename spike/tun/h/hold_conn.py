#!/usr/bin/env python3
"""R16-H D13 probe: hold one TUN-visible connection open until it is closed."""
import argparse
import socket
import time


def main():
    p = argparse.ArgumentParser()
    p.add_argument("--host", required=True)
    p.add_argument("--port", type=int, default=7102)
    p.add_argument("--timeout", type=float, default=15)
    args = p.parse_args()
    start = time.time()
    try:
        s = socket.create_connection((args.host, args.port), timeout=4)
        s.settimeout(args.timeout)
        print("connected %.3f" % (time.time() - start), flush=True)
        try:
            data = s.recv(1)
            print("closed %.3f eof=%s" % (time.time() - start, data == b""), flush=True)
        except socket.timeout:
            print("still-open %.3f" % (time.time() - start), flush=True)
        finally:
            s.close()
    except OSError as exc:
        print("connect-error %.3f %s" % (time.time() - start, exc), flush=True)


if __name__ == "__main__":
    main()
