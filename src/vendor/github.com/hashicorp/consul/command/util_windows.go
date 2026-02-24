// Copyright IBM Corp. 2017, 2023
// SPDX-License-Identifier: MPL-2.0

// +build windows

package command

import (
	"os"
	"syscall"
)

// signalPid sends a sig signal to the process with process id pid.
// Since interrupts et al is not implemented on Windows, signalPid
// always sends a SIGKILL signal irrespective of the sig value.
func signalPid(pid int, sig syscall.Signal) error {
	p, err := os.FindProcess(pid)
	if err != nil {
		return err
	}
	_ = sig
	return p.Signal(syscall.SIGKILL)
}
