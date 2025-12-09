// Copyright IBM Corp. 2017, 2023
// SPDX-License-Identifier: MPL-2.0

package testutil

import (
	"time"
)

func Yield() {
	time.Sleep(10 * time.Millisecond)
}
