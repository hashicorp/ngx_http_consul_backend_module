#!/usr/bin/env bash
# Copyright (c) HashiCorp, Inc.
# SPDX-License-Identifier: MPL-2.0

set -e

# Read from the file we created
SERVER_COUNT=$(cat /tmp/consul-server-count | tr -d '\n')

# Write the flags to a temporary file
cat >/tmp/consul_flags << EOF
export CONSUL_FLAGS="-server -bootstrap-expect=${SERVER_COUNT} -data-dir=/mnt/consul"
EOF

# Write it to the full service file
sudo mv /tmp/consul_flags /etc/service/consul
chmod 0644 /etc/service/consul
