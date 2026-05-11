// Copyright IBM Corp. 2017, 2026
// SPDX-License-Identifier: MPL-2.0

// +build !windows

package command

import (
	"syscall"
)

// signalPid sends a sig signal to the process with process id pid.
func signalPid(pid int, sig syscall.Signal) error {
	return syscall.Kill(pid, sig)
}
