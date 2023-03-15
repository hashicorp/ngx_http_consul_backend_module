# Copyright (c) HashiCorp, Inc.
# SPDX-License-Identifier: MPL-2.0

output "nodes_floating_ips" {
  value = "${join(\",\", openstack_compute_instance_v2.consul_node.*.floating_ip)}"
}
