// Copyright (c) HashiCorp, Inc.
// SPDX-License-Identifier: MPL-2.0

package command

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/ioutil"
	"os"
	"strings"

	"github.com/hashicorp/consul/api"
)

// KVImportCommand is a Command implementation that is used to import
// a KV tree stored as JSON
type KVImportCommand struct {
	BaseCommand

	// testStdin is the input for testing.
	testStdin io.Reader
}

func (c *KVImportCommand) Synopsis() string {
	return "Imports a tree stored as JSON to the KV store"
}

func (c *KVImportCommand) Help() string {
	helpText := `
Usage: consul kv import [DATA]

  Imports key-value pairs to the key-value store from the JSON representation
  generated by the "consul kv export" command.

  The data can be read from a file by prefixing the filename with the "@"
  symbol. For example:

      $ consul kv import @filename.json

  Or it can be read from stdin using the "-" symbol:

      $ cat filename.json | consul kv import -

  Alternatively the data may be provided as the final parameter to the command,
  though care must be taken with regards to shell escaping.

  For a full list of options and examples, please see the Consul documentation.

` + c.BaseCommand.Help()

	return strings.TrimSpace(helpText)
}

func (c *KVImportCommand) Run(args []string) int {
	f := c.BaseCommand.NewFlagSet(c)

	if err := c.BaseCommand.Parse(args); err != nil {
		return 1
	}

	// Check for arg validation
	args = f.Args()
	data, err := c.dataFromArgs(args)
	if err != nil {
		c.UI.Error(fmt.Sprintf("Error! %s", err))
		return 1
	}

	// Create and test the HTTP client
	client, err := c.BaseCommand.HTTPClient()
	if err != nil {
		c.UI.Error(fmt.Sprintf("Error connecting to Consul agent: %s", err))
		return 1
	}

	var entries []*kvExportEntry
	if err := json.Unmarshal([]byte(data), &entries); err != nil {
		c.UI.Error(fmt.Sprintf("Cannot unmarshal data: %s", err))
		return 1
	}

	for _, entry := range entries {
		value, err := base64.StdEncoding.DecodeString(entry.Value)
		if err != nil {
			c.UI.Error(fmt.Sprintf("Error base 64 decoding value for key %s: %s", entry.Key, err))
			return 1
		}

		pair := &api.KVPair{
			Key:   entry.Key,
			Flags: entry.Flags,
			Value: value,
		}

		if _, err := client.KV().Put(pair, nil); err != nil {
			c.UI.Error(fmt.Sprintf("Error! Failed writing data for key %s: %s", pair.Key, err))
			return 1
		}

		c.UI.Info(fmt.Sprintf("Imported: %s", pair.Key))
	}

	return 0
}

func (c *KVImportCommand) dataFromArgs(args []string) (string, error) {
	var stdin io.Reader = os.Stdin
	if c.testStdin != nil {
		stdin = c.testStdin
	}

	switch len(args) {
	case 0:
		return "", errors.New("Missing DATA argument")
	case 1:
	default:
		return "", fmt.Errorf("Too many arguments (expected 1, got %d)", len(args))
	}

	data := args[0]

	if len(data) == 0 {
		return "", errors.New("Empty DATA argument")
	}

	switch data[0] {
	case '@':
		data, err := ioutil.ReadFile(data[1:])
		if err != nil {
			return "", fmt.Errorf("Failed to read file: %s", err)
		}
		return string(data), nil
	case '-':
		if len(data) > 1 {
			return data, nil
		}
		var b bytes.Buffer
		if _, err := io.Copy(&b, stdin); err != nil {
			return "", fmt.Errorf("Failed to read stdin: %s", err)
		}
		return b.String(), nil
	default:
		return data, nil
	}
}
