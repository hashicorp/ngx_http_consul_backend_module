# Copyright IBM Corp. 2017, 2023
# SPDX-License-Identifier: MPL-2.0

output "nodes_floating_ips" {
  value = "${join(\",\", openstack_compute_instance_v2.consul_node.*.floating_ip)}"
}
