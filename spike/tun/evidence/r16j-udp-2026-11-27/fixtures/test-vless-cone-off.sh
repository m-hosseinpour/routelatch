#!/bin/sh
set -eu
cd /w
ip addr add 93.184.216.34/32 dev lo 2>/dev/null || true
python3 udp_echo.py 9000 93.184.216.34 >/w/out/echo-cone.log 2>&1 &
sleep 0.3
XRAY_CONE_DISABLED=true /w/bin/xray run -c /w/conf/xray-vless-server.json >/w/out/vless-cone-server.log 2>&1 &
sleep 0.8
XRAY_CONE_DISABLED=true /w/bin/xray run -c /w/conf/xray-vless-client.json >/w/out/vless-cone-client.log 2>&1 &
sleep 0.8
python3 socks_udp.py 127.0.0.1:10002 93.184.216.34:9000 vless-plain-udp
