// Copyright (c) HashiCorp, Inc.
// SPDX-License-Identifier: MPL-2.0

// +build !windows

package agent

import (
	"os"
	"os/exec"
	"syscall"
)

// ExecScript returns a command to execute a script through a shell.
func ExecScript(script string) (*exec.Cmd, error) {
	shell := "/bin/sh"
	if other := os.Getenv("SHELL"); other != "" {
		shell = other
	}
	return exec.Command(shell, "-c", script), nil
}

func SetSysProcAttr(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

// KillCommandSubtree kills the command process and any child processes
func KillCommandSubtree(cmd *exec.Cmd) error {
	return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
}
