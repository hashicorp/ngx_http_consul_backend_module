# Copyright IBM Corp. 2017, 2023
# SPDX-License-Identifier: MPL-2.0

output "first_consul_node_address" {
  value = "${digitalocean_droplet.consul.0.ipv4_address}"
}

output "all_addresses" {
  value = ["${digitalocean_droplet.consul.*.ipv4_address}"]
}
