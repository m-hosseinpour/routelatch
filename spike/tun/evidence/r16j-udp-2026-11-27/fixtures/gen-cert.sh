#!/bin/sh
# Test-only self-signed certificate for the local hysteria2 fixture.
# Not a product credential and never used outside the disposable container.
set -eu
cd "$(dirname "$0")"
openssl req -x509 -newkey rsa:2048 -nodes \
  -keyout conf/key.pem -out conf/cert.pem -days 2 \
  -subj "/CN=localhost" -addext "subjectAltName=DNS:localhost"
