// Copyright (c) HashiCorp, Inc.
// SPDX-License-Identifier: MPL-2.0

package command

import (
	"strings"
	"testing"

	"github.com/hashicorp/consul/agent"
	"github.com/mitchellh/cli"
)

func testJoinCommand(t *testing.T) (*cli.MockUi, *JoinCommand) {
	ui := cli.NewMockUi()
	return ui, &JoinCommand{
		BaseCommand: BaseCommand{
			UI:    ui,
			Flags: FlagSetClientHTTP,
		},
	}
}

func TestJoinCommand_implements(t *testing.T) {
	t.Parallel()
	var _ cli.Command = &JoinCommand{}
}

func TestJoinCommandRun(t *testing.T) {
	t.Parallel()
	a1 := agent.NewTestAgent(t.Name(), ``)
	a2 := agent.NewTestAgent(t.Name(), ``)
	defer a1.Shutdown()
	defer a2.Shutdown()

	ui, c := testJoinCommand(t)
	args := []string{
		"-http-addr=" + a1.HTTPAddr(),
		a2.Config.SerfBindAddrLAN.String(),
	}

	code := c.Run(args)
	if code != 0 {
		t.Fatalf("bad: %d. %#v", code, ui.ErrorWriter.String())
	}

	if len(a1.LANMembers()) != 2 {
		t.Fatalf("bad: %#v", a1.LANMembers())
	}
}

func TestJoinCommandRun_wan(t *testing.T) {
	t.Parallel()
	a1 := agent.NewTestAgent(t.Name(), ``)
	a2 := agent.NewTestAgent(t.Name(), ``)
	defer a1.Shutdown()
	defer a2.Shutdown()

	ui, c := testJoinCommand(t)
	args := []string{
		"-http-addr=" + a1.HTTPAddr(),
		"-wan",
		a2.Config.SerfBindAddrWAN.String(),
	}

	code := c.Run(args)
	if code != 0 {
		t.Fatalf("bad: %d. %#v", code, ui.ErrorWriter.String())
	}

	if len(a1.WANMembers()) != 2 {
		t.Fatalf("bad: %#v", a1.WANMembers())
	}
}

func TestJoinCommandRun_noAddrs(t *testing.T) {
	t.Parallel()
	ui, c := testJoinCommand(t)
	args := []string{"-http-addr=foo"}

	code := c.Run(args)
	if code != 1 {
		t.Fatalf("bad: %d", code)
	}

	if !strings.Contains(ui.ErrorWriter.String(), "one address") {
		t.Fatalf("bad: %#v", ui.ErrorWriter.String())
	}
}
