#!/bin/bash
# Copyright (c) HashiCorp, Inc.
# SPDX-License-Identifier: MPL-2.0

set -e

openssl req -new -sha256 -nodes -out Alice.csr -newkey rsa:2048 -keyout Alice.key -config Alice.cfg
openssl ca -batch -config myca.conf -extfile Alice.ext -notext -in Alice.csr -out Alice.crt
rm Alice.csr
