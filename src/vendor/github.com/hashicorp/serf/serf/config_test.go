// Copyright IBM Corp. 2017, 2023
// SPDX-License-Identifier: MPL-2.0

package serf

import (
	"testing"
)

func TestDefaultConfig(t *testing.T) {
	c := DefaultConfig()
	if c.ProtocolVersion != 4 {
		t.Fatalf("bad: %#v", c)
	}
}
