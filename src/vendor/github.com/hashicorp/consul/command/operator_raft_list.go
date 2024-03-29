// Copyright (c) HashiCorp, Inc.
// SPDX-License-Identifier: MPL-2.0

package command

import (
	"flag"
	"fmt"
	"strings"

	"github.com/hashicorp/consul/api"
	"github.com/ryanuber/columnize"
)

type OperatorRaftListCommand struct {
	BaseCommand
}

func (c *OperatorRaftListCommand) Help() string {
	helpText := `
Usage: consul operator raft list-peers [options]

Displays the current Raft peer configuration.

` + c.BaseCommand.Help()

	return strings.TrimSpace(helpText)
}

func (c *OperatorRaftListCommand) Synopsis() string {
	return "Display the current Raft peer configuration"
}

func (c *OperatorRaftListCommand) Run(args []string) int {
	c.BaseCommand.NewFlagSet(c)

	if err := c.BaseCommand.Parse(args); err != nil {
		if err == flag.ErrHelp {
			return 0
		}
		c.UI.Error(fmt.Sprintf("Failed to parse args: %v", err))
		return 1
	}

	// Set up a client.
	client, err := c.BaseCommand.HTTPClient()
	if err != nil {
		c.UI.Error(fmt.Sprintf("Error initializing client: %s", err))
		return 1
	}

	// Fetch the current configuration.
	result, err := raftListPeers(client, c.BaseCommand.HTTPStale())
	if err != nil {
		c.UI.Error(fmt.Sprintf("Error getting peers: %v", err))
		return 1
	}

	c.UI.Output(result)
	return 0
}

func raftListPeers(client *api.Client, stale bool) (string, error) {

	q := &api.QueryOptions{
		AllowStale: stale,
	}
	reply, err := client.Operator().RaftGetConfiguration(q)
	if err != nil {
		return "", fmt.Errorf("Failed to retrieve raft configuration: %v", err)
	}

	// Format it as a nice table.
	result := []string{"Node|ID|Address|State|Voter|RaftProtocol"}
	for _, s := range reply.Servers {
		raftProtocol := s.ProtocolVersion

		if raftProtocol == "" {
			raftProtocol = "<=1"
		}
		state := "follower"
		if s.Leader {
			state = "leader"
		}
		result = append(result, fmt.Sprintf("%s|%s|%s|%s|%v|%s",
			s.Node, s.ID, s.Address, state, s.Voter, raftProtocol))
	}

	return columnize.SimpleFormat(result), nil
}
