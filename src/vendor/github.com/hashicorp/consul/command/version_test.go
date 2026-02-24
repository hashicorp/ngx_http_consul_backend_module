// Copyright IBM Corp. 2017, 2023
// SPDX-License-Identifier: MPL-2.0

package command

import (
	"testing"

	"github.com/mitchellh/cli"
)

func TestVersionCommand_implements(t *testing.T) {
	t.Parallel()
	var _ cli.Command = &VersionCommand{}
}
