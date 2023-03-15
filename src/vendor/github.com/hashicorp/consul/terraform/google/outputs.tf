# Copyright (c) HashiCorp, Inc.
# SPDX-License-Identifier: MPL-2.0

output "server_address" {
    value = "${google_compute_instance.consul.0.network_interface.0.address}"
}

