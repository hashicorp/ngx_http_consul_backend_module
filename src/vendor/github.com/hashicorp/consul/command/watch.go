// Copyright (c) HashiCorp, Inc.
// SPDX-License-Identifier: MPL-2.0

package command

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"

	"github.com/hashicorp/consul/agent"
	"github.com/hashicorp/consul/watch"
)

// WatchCommand is a Command implementation that is used to setup
// a "watch" which uses a sub-process
type WatchCommand struct {
	BaseCommand
	ShutdownCh <-chan struct{}
}

func (c *WatchCommand) Help() string {
	helpText := `
Usage: consul watch [options] [child...]

  Watches for changes in a given data view from Consul. If a child process
  is specified, it will be invoked with the latest results on changes. Otherwise,
  the latest values are dumped to stdout and the watch terminates.

  Providing the watch type is required, and other parameters may be required
  or supported depending on the watch type.

` + c.BaseCommand.Help()

	return strings.TrimSpace(helpText)
}

func (c *WatchCommand) Run(args []string) int {
	var watchType, key, prefix, service, tag, passingOnly, state, name string
	var shell bool

	f := c.BaseCommand.NewFlagSet(c)
	f.StringVar(&watchType, "type", "",
		"Specifies the watch type. One of key, keyprefix, services, nodes, "+
			"service, checks, or event.")
	f.StringVar(&key, "key", "",
		"Specifies the key to watch. Only for 'key' type.")
	f.StringVar(&prefix, "prefix", "",
		"Specifies the key prefix to watch. Only for 'keyprefix' type.")
	f.StringVar(&service, "service", "",
		"Specifies the service to watch. Required for 'service' type, "+
			"optional for 'checks' type.")
	f.StringVar(&tag, "tag", "",
		"Specifies the service tag to filter on. Optional for 'service' type.")
	f.StringVar(&passingOnly, "passingonly", "",
		"Specifies if only hosts passing all checks are displayed. "+
			"Optional for 'service' type, must be one of `[true|false]`. Defaults false.")
	f.BoolVar(&shell, "shell", true,
		"Use a shell to run the command (can set a custom shell via the SHELL "+
			"environment variable).")
	f.StringVar(&state, "state", "",
		"Specifies the states to watch. Optional for 'checks' type.")
	f.StringVar(&name, "name", "",
		"Specifies an event name to watch. Only for 'event' type.")

	if err := c.BaseCommand.Parse(args); err != nil {
		return 1
	}

	// Check for a type
	if watchType == "" {
		c.UI.Error("Watch type must be specified")
		c.UI.Error("")
		c.UI.Error(c.Help())
		return 1
	}

	// Compile the watch parameters
	params := make(map[string]interface{})
	if watchType != "" {
		params["type"] = watchType
	}
	if c.BaseCommand.HTTPDatacenter() != "" {
		params["datacenter"] = c.BaseCommand.HTTPDatacenter()
	}
	if c.BaseCommand.HTTPToken() != "" {
		params["token"] = c.BaseCommand.HTTPToken()
	}
	if key != "" {
		params["key"] = key
	}
	if prefix != "" {
		params["prefix"] = prefix
	}
	if service != "" {
		params["service"] = service
	}
	if tag != "" {
		params["tag"] = tag
	}
	if c.BaseCommand.HTTPStale() {
		params["stale"] = c.BaseCommand.HTTPStale()
	}
	if state != "" {
		params["state"] = state
	}
	if name != "" {
		params["name"] = name
	}
	if passingOnly != "" {
		b, err := strconv.ParseBool(passingOnly)
		if err != nil {
			c.UI.Error(fmt.Sprintf("Failed to parse passingonly flag: %s", err))
			return 1
		}
		params["passingonly"] = b
	}

	// Create the watch
	wp, err := watch.Parse(params)
	if err != nil {
		c.UI.Error(fmt.Sprintf("%s", err))
		return 1
	}

	// Create and test the HTTP client
	client, err := c.BaseCommand.HTTPClient()
	if err != nil {
		c.UI.Error(fmt.Sprintf("Error connecting to Consul agent: %s", err))
		return 1
	}
	_, err = client.Agent().NodeName()
	if err != nil {
		c.UI.Error(fmt.Sprintf("Error querying Consul agent: %s", err))
		return 1
	}

	// Setup handler

	// errExit:
	//	0: false
	//	1: true
	errExit := 0
	if len(f.Args()) == 0 {
		wp.Handler = func(idx uint64, data interface{}) {
			defer wp.Stop()
			buf, err := json.MarshalIndent(data, "", "    ")
			if err != nil {
				c.UI.Error(fmt.Sprintf("Error encoding output: %s", err))
				errExit = 1
			}
			c.UI.Output(string(buf))
		}
	} else {
		wp.Handler = func(idx uint64, data interface{}) {
			doneCh := make(chan struct{})
			defer close(doneCh)
			logFn := func(err error) {
				c.UI.Error(fmt.Sprintf("Warning, could not forward signal: %s", err))
			}

			// Create the command
			var buf bytes.Buffer
			var err error
			var cmd *exec.Cmd
			if !shell {
				cmd, err = agent.ExecSubprocess(f.Args())
			} else {
				cmd, err = agent.ExecScript(strings.Join(f.Args(), " "))
			}
			if err != nil {
				c.UI.Error(fmt.Sprintf("Error executing handler: %s", err))
				goto ERR
			}
			cmd.Env = append(os.Environ(),
				"CONSUL_INDEX="+strconv.FormatUint(idx, 10),
			)

			// Encode the input
			if err = json.NewEncoder(&buf).Encode(data); err != nil {
				c.UI.Error(fmt.Sprintf("Error encoding output: %s", err))
				goto ERR
			}
			cmd.Stdin = &buf
			cmd.Stdout = os.Stdout
			cmd.Stderr = os.Stderr

			// Run the handler.
			if err := cmd.Start(); err != nil {
				c.UI.Error(fmt.Sprintf("Error starting handler: %s", err))
				goto ERR
			}

			// Set up signal forwarding.
			agent.ForwardSignals(cmd, logFn, doneCh)

			// Wait for the handler to complete.
			if err := cmd.Wait(); err != nil {
				c.UI.Error(fmt.Sprintf("Error executing handler: %s", err))
				goto ERR
			}
			return
		ERR:
			wp.Stop()
			errExit = 1
		}
	}

	// Watch for a shutdown
	go func() {
		<-c.ShutdownCh
		wp.Stop()
		os.Exit(0)
	}()

	// Run the watch
	if err := wp.Run(c.BaseCommand.HTTPAddr()); err != nil {
		c.UI.Error(fmt.Sprintf("Error querying Consul agent: %s", err))
		return 1
	}

	return errExit
}

func (c *WatchCommand) Synopsis() string {
	return "Watch for changes in Consul"
}
