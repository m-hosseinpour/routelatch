#!/bin/sh
set -eu
cd /w
python3 udp_echo.py 9000 >/w/out/echo.log 2>&1 &
sleep 0.3
/w/bin/sing-box run -c /w/conf/singbox-mixed-direct.json >/w/out/singbox-direct.log 2>&1 &
sleep 0.8
python3 socks_udp.py 127.0.0.1:10003 127.0.0.1:9000 hello-socks5-exit
