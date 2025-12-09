#!/usr/bin/env bash
# Copyright IBM Corp. 2017, 2023
# SPDX-License-Identifier: MPL-2.0

set -e

# Get the parent directory of where this script is.
SOURCE="${BASH_SOURCE[0]}"
while [ -h "$SOURCE" ] ; do SOURCE="$(readlink "$SOURCE")"; done
DIR="$( cd -P "$( dirname "$SOURCE" )/.." && pwd )"

# Change into that dir because we expect that.
cd $DIR

# Make sure build tools are available.
make tools

# Build the web assets.
pushd ui
bundle
make dist
popd

# Make the static assets using the container version of the builder
make static-assets

exit 0
