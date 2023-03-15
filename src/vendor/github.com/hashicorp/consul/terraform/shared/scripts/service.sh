#!/usr/bin/env bash
# Copyright (c) HashiCorp, Inc.
# SPDX-License-Identifier: MPL-2.0

set -e

echo "Starting Consul..."
if [ -x "$(command -v systemctl)" ]; then
  echo "using systemctl"
  sudo systemctl enable consul.service
  sudo systemctl start consul
else 
  echo "using upstart"
  sudo start consul
fi
