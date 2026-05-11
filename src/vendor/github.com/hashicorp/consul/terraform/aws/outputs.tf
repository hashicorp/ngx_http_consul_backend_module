# Copyright IBM Corp. 2017, 2026
# SPDX-License-Identifier: MPL-2.0

output "server_address" {
    value = "${aws_instance.server.0.public_dns}"
}
