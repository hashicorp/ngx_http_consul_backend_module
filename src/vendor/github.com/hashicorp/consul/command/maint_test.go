// Copyright (c) HashiCorp, Inc.
// SPDX-License-Identifier: MPL-2.0

package command

import (
	"strings"
	"testing"

	"github.com/hashicorp/consul/agent"
	"github.com/hashicorp/consul/agent/structs"
	"github.com/mitchellh/cli"
)

func testMaintCommand(t *testing.T) (*cli.MockUi, *MaintCommand) {
	ui := cli.NewMockUi()
	return ui, &MaintCommand{
		BaseCommand: BaseCommand{
			UI:    ui,
			Flags: FlagSetClientHTTP,
		},
	}
}

func TestMaintCommand_implements(t *testing.T) {
	t.Parallel()
	var _ cli.Command = &MaintCommand{}
}

func TestMaintCommandRun_ConflictingArgs(t *testing.T) {
	t.Parallel()
	_, c := testMaintCommand(t)

	if code := c.Run([]string{"-enable", "-disable"}); code != 1 {
		t.Fatalf("expected return code 1, got %d", code)
	}

	if code := c.Run([]string{"-disable", "-reason=broken"}); code != 1 {
		t.Fatalf("expected return code 1, got %d", code)
	}

	if code := c.Run([]string{"-reason=broken"}); code != 1 {
		t.Fatalf("expected return code 1, got %d", code)
	}

	if code := c.Run([]string{"-service=redis"}); code != 1 {
		t.Fatalf("expected return code 1, got %d", code)
	}
}

func TestMaintCommandRun_NoArgs(t *testing.T) {
	t.Parallel()
	a := agent.NewTestAgent(t.Name(), ``)
	defer a.Shutdown()

	// Register the service and put it into maintenance mode
	service := &structs.NodeService{
		ID:      "test",
		Service: "test",
	}
	if err := a.AddService(service, nil, false, ""); err != nil {
		t.Fatalf("err: %v", err)
	}
	if err := a.EnableServiceMaintenance("test", "broken 1", ""); err != nil {
		t.Fatalf("err: %s", err)
	}

	// Enable node maintenance
	a.EnableNodeMaintenance("broken 2", "")

	// Run consul maint with no args (list mode)
	ui, c := testMaintCommand(t)

	args := []string{"-http-addr=" + a.HTTPAddr()}
	code := c.Run(args)
	if code != 0 {
		t.Fatalf("bad: %d. %#v", code, ui.ErrorWriter.String())
	}

	// Ensure the service shows up in the list
	out := ui.OutputWriter.String()
	if !strings.Contains(out, "test") {
		t.Fatalf("bad:\n%s", out)
	}
	if !strings.Contains(out, "broken 1") {
		t.Fatalf("bad:\n%s", out)
	}

	// Ensure the node shows up in the list
	if !strings.Contains(out, a.Config.NodeName) {
		t.Fatalf("bad:\n%s", out)
	}
	if !strings.Contains(out, "broken 2") {
		t.Fatalf("bad:\n%s", out)
	}
}

func TestMaintCommandRun_EnableNodeMaintenance(t *testing.T) {
	t.Parallel()
	a := agent.NewTestAgent(t.Name(), ``)
	defer a.Shutdown()

	ui, c := testMaintCommand(t)

	args := []string{
		"-http-addr=" + a.HTTPAddr(),
		"-enable",
		"-reason=broken",
	}
	code := c.Run(args)
	if code != 0 {
		t.Fatalf("bad: %d. %#v", code, ui.ErrorWriter.String())
	}

	if !strings.Contains(ui.OutputWriter.String(), "now enabled") {
		t.Fatalf("bad: %#v", ui.OutputWriter.String())
	}
}

func TestMaintCommandRun_DisableNodeMaintenance(t *testing.T) {
	t.Parallel()
	a := agent.NewTestAgent(t.Name(), ``)
	defer a.Shutdown()

	ui, c := testMaintCommand(t)

	args := []string{
		"-http-addr=" + a.HTTPAddr(),
		"-disable",
	}
	code := c.Run(args)
	if code != 0 {
		t.Fatalf("bad: %d. %#v", code, ui.ErrorWriter.String())
	}

	if !strings.Contains(ui.OutputWriter.String(), "now disabled") {
		t.Fatalf("bad: %#v", ui.OutputWriter.String())
	}
}

func TestMaintCommandRun_EnableServiceMaintenance(t *testing.T) {
	t.Parallel()
	a := agent.NewTestAgent(t.Name(), ``)
	defer a.Shutdown()

	// Register the service
	service := &structs.NodeService{
		ID:      "test",
		Service: "test",
	}
	if err := a.AddService(service, nil, false, ""); err != nil {
		t.Fatalf("err: %v", err)
	}

	ui, c := testMaintCommand(t)

	args := []string{
		"-http-addr=" + a.HTTPAddr(),
		"-enable",
		"-service=test",
		"-reason=broken",
	}
	code := c.Run(args)
	if code != 0 {
		t.Fatalf("bad: %d. %#v", code, ui.ErrorWriter.String())
	}

	if !strings.Contains(ui.OutputWriter.String(), "now enabled") {
		t.Fatalf("bad: %#v", ui.OutputWriter.String())
	}
}

func TestMaintCommandRun_DisableServiceMaintenance(t *testing.T) {
	t.Parallel()
	a := agent.NewTestAgent(t.Name(), ``)
	defer a.Shutdown()

	// Register the service
	service := &structs.NodeService{
		ID:      "test",
		Service: "test",
	}
	if err := a.AddService(service, nil, false, ""); err != nil {
		t.Fatalf("err: %v", err)
	}

	ui, c := testMaintCommand(t)

	args := []string{
		"-http-addr=" + a.HTTPAddr(),
		"-disable",
		"-service=test",
	}
	code := c.Run(args)
	if code != 0 {
		t.Fatalf("bad: %d. %#v", code, ui.ErrorWriter.String())
	}

	if !strings.Contains(ui.OutputWriter.String(), "now disabled") {
		t.Fatalf("bad: %#v", ui.OutputWriter.String())
	}
}

func TestMaintCommandRun_ServiceMaintenance_NoService(t *testing.T) {
	t.Parallel()
	a := agent.NewTestAgent(t.Name(), ``)
	defer a.Shutdown()

	ui, c := testMaintCommand(t)

	args := []string{
		"-http-addr=" + a.HTTPAddr(),
		"-enable",
		"-service=redis",
		"-reason=broken",
	}
	code := c.Run(args)
	if code != 1 {
		t.Fatalf("expected response code 1, got %d", code)
	}

	if !strings.Contains(ui.ErrorWriter.String(), "No service registered") {
		t.Fatalf("bad: %#v", ui.ErrorWriter.String())
	}
}
