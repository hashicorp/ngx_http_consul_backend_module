// Copyright (c) HashiCorp, Inc.
// SPDX-License-Identifier: MPL-2.0

package command

import (
	"bufio"
	"bytes"
	"flag"
	"fmt"
	"io"
	"strings"

	"github.com/hashicorp/consul/api"
	"github.com/hashicorp/consul/configutil"
	"github.com/mitchellh/cli"
	text "github.com/tonnerre/golang-text"
)

// maxLineLength is the maximum width of any line.
const maxLineLength int = 72

// FlagSetFlags is an enum to define what flags are present in the
// default FlagSet returned.
type FlagSetFlags uint

const (
	FlagSetNone       FlagSetFlags = 1 << iota
	FlagSetClientHTTP FlagSetFlags = 1 << iota
	FlagSetServerHTTP FlagSetFlags = 1 << iota

	FlagSetHTTP = FlagSetClientHTTP | FlagSetServerHTTP
)

type BaseCommand struct {
	UI    cli.Ui
	Flags FlagSetFlags

	HideNormalFlagsHelp bool

	flagSet *flag.FlagSet

	// These are the options which correspond to the HTTP API options
	httpAddr      configutil.StringValue
	token         configutil.StringValue
	caFile        configutil.StringValue
	caPath        configutil.StringValue
	certFile      configutil.StringValue
	keyFile       configutil.StringValue
	tlsServerName configutil.StringValue

	datacenter configutil.StringValue
	stale      configutil.BoolValue
}

// HTTPClient returns a client with the parsed flags. It panics if the command
// does not accept HTTP flags or if the flags have not been parsed.
func (c *BaseCommand) HTTPClient() (*api.Client, error) {
	if !c.hasClientHTTP() && !c.hasServerHTTP() {
		panic("no http flags defined")
	}
	if !c.flagSet.Parsed() {
		panic("flags have not been parsed")
	}

	config := api.DefaultConfig()
	c.MergeHTTPConfig(config)
	return api.NewClient(config)
}

func (c *BaseCommand) MergeHTTPConfig(config *api.Config) {
	c.httpAddr.Merge(&config.Address)
	c.token.Merge(&config.Token)
	c.caFile.Merge(&config.TLSConfig.CAFile)
	c.caPath.Merge(&config.TLSConfig.CAPath)
	c.certFile.Merge(&config.TLSConfig.CertFile)
	c.keyFile.Merge(&config.TLSConfig.KeyFile)
	c.tlsServerName.Merge(&config.TLSConfig.Address)
	c.datacenter.Merge(&config.Datacenter)
}

func (c *BaseCommand) HTTPAddr() string {
	return c.httpAddr.String()
}

func (c *BaseCommand) HTTPToken() string {
	return c.token.String()
}

func (c *BaseCommand) HTTPDatacenter() string {
	return c.datacenter.String()
}

func (c *BaseCommand) HTTPStale() bool {
	var stale bool
	c.stale.Merge(&stale)
	return stale
}

// httpFlagsClient is the list of flags that apply to HTTP connections.
func (c *BaseCommand) httpFlagsClient() *flag.FlagSet {
	f := flag.NewFlagSet("", flag.ContinueOnError)
	f.Var(&c.caFile, "ca-file",
		"Path to a CA file to use for TLS when communicating with Consul. This "+
			"can also be specified via the CONSUL_CACERT environment variable.")
	f.Var(&c.caPath, "ca-path",
		"Path to a directory of CA certificates to use for TLS when communicating "+
			"with Consul. This can also be specified via the CONSUL_CAPATH environment variable.")
	f.Var(&c.certFile, "client-cert",
		"Path to a client cert file to use for TLS when 'verify_incoming' is enabled. This "+
			"can also be specified via the CONSUL_CLIENT_CERT environment variable.")
	f.Var(&c.keyFile, "client-key",
		"Path to a client key file to use for TLS when 'verify_incoming' is enabled. This "+
			"can also be specified via the CONSUL_CLIENT_KEY environment variable.")
	f.Var(&c.httpAddr, "http-addr",
		"The `address` and port of the Consul HTTP agent. The value can be an IP "+
			"address or DNS address, but it must also include the port. This can "+
			"also be specified via the CONSUL_HTTP_ADDR environment variable. The "+
			"default value is http://127.0.0.1:8500. The scheme can also be set to "+
			"HTTPS by setting the environment variable CONSUL_HTTP_SSL=true.")
	f.Var(&c.token, "token",
		"ACL token to use in the request. This can also be specified via the "+
			"CONSUL_HTTP_TOKEN environment variable. If unspecified, the query will "+
			"default to the token of the Consul agent at the HTTP address.")
	f.Var(&c.tlsServerName, "tls-server-name",
		"The server name to use as the SNI host when connecting via TLS. This "+
			"can also be specified via the CONSUL_TLS_SERVER_NAME environment variable.")
	return f
}

// httpFlagsServer is the list of flags that apply to HTTP connections.
func (c *BaseCommand) httpFlagsServer() *flag.FlagSet {
	f := flag.NewFlagSet("", flag.ContinueOnError)
	f.Var(&c.datacenter, "datacenter",
		"Name of the datacenter to query. If unspecified, this will default to "+
			"the datacenter of the queried agent.")
	f.Var(&c.stale, "stale",
		"Permit any Consul server (non-leader) to respond to this request. This "+
			"allows for lower latency and higher throughput, but can result in "+
			"stale data. This option has no effect on non-read operations. The "+
			"default value is false.")
	return f
}

// NewFlagSet creates a new flag set for the given command. It automatically
// generates help output and adds the appropriate API flags.
func (c *BaseCommand) NewFlagSet(command cli.Command) *flag.FlagSet {
	c.flagSet = flag.NewFlagSet("", flag.ContinueOnError)
	c.flagSet.Usage = func() { c.UI.Error(command.Help()) }

	if c.hasClientHTTP() {
		c.httpFlagsClient().VisitAll(func(f *flag.Flag) {
			c.flagSet.Var(f.Value, f.Name, f.DefValue)
		})
	}

	if c.hasServerHTTP() {
		c.httpFlagsServer().VisitAll(func(f *flag.Flag) {
			c.flagSet.Var(f.Value, f.Name, f.DefValue)
		})
	}

	errR, errW := io.Pipe()
	errScanner := bufio.NewScanner(errR)
	go func() {
		for errScanner.Scan() {
			c.UI.Error(errScanner.Text())
		}
	}()
	c.flagSet.SetOutput(errW)

	return c.flagSet
}

// Parse is used to parse the underlying flag set.
func (c *BaseCommand) Parse(args []string) error {
	return c.flagSet.Parse(args)
}

// Help returns the help for this flagSet.
func (c *BaseCommand) Help() string {
	// Some commands with subcommands (kv/snapshot) call this without initializing
	// any flags first, so exit early to avoid a panic
	if c.flagSet == nil {
		return ""
	}
	return c.helpFlagsFor(c.flagSet)
}

// hasClientHTTP returns true if this meta command contains client HTTP flags.
func (c *BaseCommand) hasClientHTTP() bool {
	return c.Flags&FlagSetClientHTTP != 0
}

// hasServerHTTP returns true if this meta command contains server HTTP flags.
func (c *BaseCommand) hasServerHTTP() bool {
	return c.Flags&FlagSetServerHTTP != 0
}

// helpFlagsFor visits all flags in the given flag set and prints formatted
// help output. This function is sad because there's no "merging" of command
// line flags. We explicitly pull out our "common" options into another section
// by doing string comparisons :(.
func (c *BaseCommand) helpFlagsFor(f *flag.FlagSet) string {
	httpFlagsClient := c.httpFlagsClient()
	httpFlagsServer := c.httpFlagsServer()

	var out bytes.Buffer

	if c.hasClientHTTP() || c.hasServerHTTP() {
		printTitle(&out, "HTTP API Options")
	}
	if c.hasClientHTTP() {
		httpFlagsClient.VisitAll(func(f *flag.Flag) {
			printFlag(&out, f)
		})
	}
	if c.hasServerHTTP() {
		httpFlagsServer.VisitAll(func(f *flag.Flag) {
			printFlag(&out, f)
		})
	}

	if !c.HideNormalFlagsHelp {
		firstCommand := true
		f.VisitAll(func(f *flag.Flag) {
			// Skip HTTP flags as they will be grouped separately
			if flagContains(httpFlagsClient, f) || flagContains(httpFlagsServer, f) {
				return
			}
			if firstCommand {
				printTitle(&out, "Command Options")
				firstCommand = false
			}
			printFlag(&out, f)
		})
	}

	return strings.TrimRight(out.String(), "\n")
}

// printTitle prints a consistently-formatted title to the given writer.
func printTitle(w io.Writer, s string) {
	fmt.Fprintf(w, "%s\n\n", s)
}

// printFlag prints a single flag to the given writer.
func printFlag(w io.Writer, f *flag.Flag) {
	example, _ := flag.UnquoteUsage(f)
	if example != "" {
		fmt.Fprintf(w, "  -%s=<%s>\n", f.Name, example)
	} else {
		fmt.Fprintf(w, "  -%s\n", f.Name)
	}

	indented := wrapAtLength(f.Usage, 5)
	fmt.Fprintf(w, "%s\n\n", indented)
}

// flagContains returns true if the given flag is contained in the given flag
// set or false otherwise.
func flagContains(fs *flag.FlagSet, f *flag.Flag) bool {
	var skip bool

	fs.VisitAll(func(hf *flag.Flag) {
		if skip {
			return
		}

		if f.Name == hf.Name {
			skip = true
			return
		}
	})

	return skip
}

// wrapAtLength wraps the given text at the maxLineLength, taking into account
// any provided left padding.
func wrapAtLength(s string, pad int) string {
	wrapped := text.Wrap(s, maxLineLength-pad)
	lines := strings.Split(wrapped, "\n")
	for i, line := range lines {
		lines[i] = strings.Repeat(" ", pad) + line
	}
	return strings.Join(lines, "\n")
}
