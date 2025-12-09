#!/usr/bin/env bash
# Copyright IBM Corp. 2017, 2023
# SPDX-License-Identifier: MPL-2.0

set -e

echo "Starting echos"
/usr/local/bin/http-echo -listen=127.0.0.1:8081 -text=1 &
/usr/local/bin/http-echo -listen=127.0.0.1:8082 -text=2 &
/usr/local/bin/http-echo -listen=127.0.0.1:8083 -text=3 &
/usr/local/bin/http-echo -listen=127.0.0.1:8084 -text=4 &
/usr/local/bin/http-echo -listen=127.0.0.1:8085 -text=5 &

echo "Starting consul"
tee /etc/consul.d/http-echo-1.json > /dev/null <<"EOF"
{ "service": { "id": "http-echo-1", "name": "http-echo", "port": 8081 } }
EOF
tee /etc/consul.d/http-echo-2.json > /dev/null <<"EOF"
{ "service": { "id": "http-echo-2", "name": "http-echo", "port": 8082 } }
EOF
tee /etc/consul.d/http-echo-3.json > /dev/null <<"EOF"
{ "service": { "id": "http-echo-3", "name": "http-echo", "port": 8083 } }
EOF
tee /etc/consul.d/http-echo-4.json > /dev/null <<"EOF"
{ "service": { "id": "http-echo-4", "name": "http-echo", "port": 8084 } }
EOF
tee /etc/consul.d/http-echo-5.json > /dev/null <<"EOF"
{ "service": { "id": "http-echo-4", "name": "http-echo", "port": 8084 } }
EOF
/usr/local/bin/consul agent \
  -dev \
  -config-dir=/etc/consul.d/ &

echo "Starting nginx"
export LD_LIBRARY_PATH="/usr/local/nginx/ext:$LD_LIBRARY_PATH"
exec /usr/local/nginx/sbin/nginx
