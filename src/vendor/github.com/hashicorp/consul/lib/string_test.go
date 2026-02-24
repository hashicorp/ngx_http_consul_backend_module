// Copyright IBM Corp. 2017, 2023
// SPDX-License-Identifier: MPL-2.0

package lib

import (
	"testing"
)

func TestStrContains(t *testing.T) {
	l := []string{"a", "b", "c"}
	if !StrContains(l, "b") {
		t.Fatalf("should contain")
	}
	if StrContains(l, "d") {
		t.Fatalf("should not contain")
	}
}
