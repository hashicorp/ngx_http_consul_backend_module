// Copyright (c) HashiCorp, Inc.
// SPDX-License-Identifier: MPL-2.0

package agent

import (
	"context"
	"crypto/sha512"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"io/ioutil"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/armon/go-metrics"
	"github.com/hashicorp/consul/acl"
	"github.com/hashicorp/consul/agent/config"
	"github.com/hashicorp/consul/agent/consul"
	"github.com/hashicorp/consul/agent/structs"
	"github.com/hashicorp/consul/agent/systemd"
	"github.com/hashicorp/consul/agent/token"
	"github.com/hashicorp/consul/api"
	"github.com/hashicorp/consul/ipaddr"
	"github.com/hashicorp/consul/lib"
	"github.com/hashicorp/consul/logger"
	"github.com/hashicorp/consul/types"
	"github.com/hashicorp/consul/watch"
	"github.com/hashicorp/go-uuid"
	"github.com/hashicorp/memberlist"
	"github.com/hashicorp/raft"
	"github.com/hashicorp/serf/serf"
	"github.com/shirou/gopsutil/host"
)

const (
	// Path to save agent service definitions
	servicesDir = "services"

	// Path to save local agent checks
	checksDir     = "checks"
	checkStateDir = "checks/state"

	// Default reasons for node/service maintenance mode
	defaultNodeMaintReason = "Maintenance mode is enabled for this node, " +
		"but no reason was provided. This is a default message."
	defaultServiceMaintReason = "Maintenance mode is enabled for this " +
		"service, but no reason was provided. This is a default message."
)

// delegate defines the interface shared by both
// consul.Client and consul.Server.
type delegate interface {
	Encrypted() bool
	GetLANCoordinate() (lib.CoordinateSet, error)
	Leave() error
	LANMembers() []serf.Member
	LANMembersAllSegments() ([]serf.Member, error)
	LANSegmentMembers(segment string) ([]serf.Member, error)
	LocalMember() serf.Member
	JoinLAN(addrs []string) (n int, err error)
	RemoveFailedNode(node string) error
	RPC(method string, args interface{}, reply interface{}) error
	SnapshotRPC(args *structs.SnapshotRequest, in io.Reader, out io.Writer, replyFn structs.SnapshotReplyFn) error
	Shutdown() error
	Stats() map[string]map[string]string
}

// notifier is called after a successful JoinLAN.
type notifier interface {
	Notify(string) error
}

// The agent is the long running process that is run on every machine.
// It exposes an RPC interface that is used by the CLI to control the
// agent. The agent runs the query interfaces like HTTP, DNS, and RPC.
// However, it can run in either a client, or server mode. In server
// mode, it runs a full Consul server. In client-only mode, it only forwards
// requests to other Consul servers.
type Agent struct {
	// config is the agent configuration.
	config *config.RuntimeConfig

	// Used for writing our logs
	logger *log.Logger

	// Output sink for logs
	LogOutput io.Writer

	// Used for streaming logs to
	LogWriter *logger.LogWriter

	// In-memory sink used for collecting metrics
	MemSink *metrics.InmemSink

	// delegate is either a *consul.Server or *consul.Client
	// depending on the configuration
	delegate delegate

	// acls is an object that helps manage local ACL enforcement.
	acls *aclManager

	// state stores a local representation of the node,
	// services and checks. Used for anti-entropy.
	state *localState

	// checkReapAfter maps the check ID to a timeout after which we should
	// reap its associated service
	checkReapAfter map[types.CheckID]time.Duration

	// checkMonitors maps the check ID to an associated monitor
	checkMonitors map[types.CheckID]*CheckMonitor

	// checkHTTPs maps the check ID to an associated HTTP check
	checkHTTPs map[types.CheckID]*CheckHTTP

	// checkTCPs maps the check ID to an associated TCP check
	checkTCPs map[types.CheckID]*CheckTCP

	// checkTTLs maps the check ID to an associated check TTL
	checkTTLs map[types.CheckID]*CheckTTL

	// checkDockers maps the check ID to an associated Docker Exec based check
	checkDockers map[types.CheckID]*CheckDocker

	// checkLock protects updates to the check* maps
	checkLock sync.Mutex

	// dockerClient is the client for performing docker health checks.
	dockerClient *DockerClient

	// eventCh is used to receive user events
	eventCh chan serf.UserEvent

	// eventBuf stores the most recent events in a ring buffer
	// using eventIndex as the next index to insert into. This
	// is guarded by eventLock. When an insert happens, the
	// eventNotify group is notified.
	eventBuf    []*UserEvent
	eventIndex  int
	eventLock   sync.RWMutex
	eventNotify NotifyGroup

	reloadCh chan chan error

	shutdown     bool
	shutdownCh   chan struct{}
	shutdownLock sync.Mutex

	// joinLANNotifier is called after a successful JoinLAN.
	joinLANNotifier notifier

	// retryJoinCh transports errors from the retry join
	// attempts.
	retryJoinCh chan error

	// endpoints maps unique RPC endpoint names to common ones
	// to allow overriding of RPC handlers since the golang
	// net/rpc server does not allow this.
	endpoints     map[string]string
	endpointsLock sync.RWMutex

	// dnsServer provides the DNS API
	dnsServers []*DNSServer

	// httpServers provides the HTTP API on various endpoints
	httpServers []*HTTPServer

	// wgServers is the wait group for all HTTP and DNS servers
	wgServers sync.WaitGroup

	// watchPlans tracks all the currently-running watch plans for the
	// agent.
	watchPlans []*watch.Plan

	// tokens holds ACL tokens initially from the configuration, but can
	// be updated at runtime, so should always be used instead of going to
	// the configuration directly.
	tokens *token.Store
}

func New(c *config.RuntimeConfig) (*Agent, error) {
	if c.Datacenter == "" {
		return nil, fmt.Errorf("Must configure a Datacenter")
	}
	if c.DataDir == "" && !c.DevMode {
		return nil, fmt.Errorf("Must configure a DataDir")
	}
	acls, err := newACLManager(c)
	if err != nil {
		return nil, err
	}

	a := &Agent{
		config:          c,
		acls:            acls,
		checkReapAfter:  make(map[types.CheckID]time.Duration),
		checkMonitors:   make(map[types.CheckID]*CheckMonitor),
		checkTTLs:       make(map[types.CheckID]*CheckTTL),
		checkHTTPs:      make(map[types.CheckID]*CheckHTTP),
		checkTCPs:       make(map[types.CheckID]*CheckTCP),
		checkDockers:    make(map[types.CheckID]*CheckDocker),
		eventCh:         make(chan serf.UserEvent, 1024),
		eventBuf:        make([]*UserEvent, 256),
		joinLANNotifier: &systemd.Notifier{},
		reloadCh:        make(chan chan error),
		retryJoinCh:     make(chan error),
		shutdownCh:      make(chan struct{}),
		endpoints:       make(map[string]string),
		tokens:          new(token.Store),
	}

	// Set up the initial state of the token store based on the config.
	a.tokens.UpdateUserToken(a.config.ACLToken)
	a.tokens.UpdateAgentToken(a.config.ACLAgentToken)
	a.tokens.UpdateAgentMasterToken(a.config.ACLAgentMasterToken)
	a.tokens.UpdateACLReplicationToken(a.config.ACLReplicationToken)

	return a, nil
}

func (a *Agent) Start() error {
	c := a.config

	logOutput := a.LogOutput
	if a.logger == nil {
		if logOutput == nil {
			logOutput = os.Stderr
		}
		a.logger = log.New(logOutput, "", log.LstdFlags)
	}

	// Retrieve or generate the node ID before setting up the rest of the
	// agent, which depends on it.
	if err := a.setupNodeID(c); err != nil {
		return fmt.Errorf("Failed to setup node ID: %v", err)
	}

	// create the local state
	a.state = NewLocalState(c, a.logger, a.tokens)

	// create the config for the rpc server/client
	consulCfg, err := a.consulConfig()
	if err != nil {
		return err
	}

	// link consul client/server with the state
	consulCfg.ServerUp = a.state.ConsulServerUp

	// Setup either the client or the server.
	if c.ServerMode {
		server, err := consul.NewServerLogger(consulCfg, a.logger, a.tokens)
		if err != nil {
			return fmt.Errorf("Failed to start Consul server: %v", err)
		}

		a.delegate = server
		a.state.delegate = server
	} else {
		client, err := consul.NewClientLogger(consulCfg, a.logger)
		if err != nil {
			return fmt.Errorf("Failed to start Consul client: %v", err)
		}

		a.delegate = client
		a.state.delegate = client
	}

	// Load checks/services/metadata.
	if err := a.loadServices(c); err != nil {
		return err
	}
	if err := a.loadChecks(c); err != nil {
		return err
	}
	if err := a.loadMetadata(c); err != nil {
		return err
	}

	// Start watching for critical services to deregister, based on their
	// checks.
	go a.reapServices()

	// Start handling events.
	go a.handleEvents()

	// Start sending network coordinate to the server.
	if !c.DisableCoordinates {
		go a.sendCoordinate()
	}

	// Write out the PID file if necessary.
	if err := a.storePid(); err != nil {
		return err
	}

	// start DNS servers
	if err := a.listenAndServeDNS(); err != nil {
		return err
	}

	// create listeners and unstarted servers
	// see comment on listenHTTP why we are doing this
	httpln, err := a.listenHTTP()
	if err != nil {
		return err
	}

	// start HTTP and HTTPS servers
	for _, l := range httpln {
		srv := NewHTTPServer(l.Addr(), a)
		if err := a.serveHTTP(l, srv); err != nil {
			return err
		}
		a.httpServers = append(a.httpServers, srv)
	}

	// register watches
	if err := a.reloadWatches(a.config); err != nil {
		return err
	}

	// start retry join
	go a.retryJoinLAN()
	go a.retryJoinWAN()

	return nil
}

func (a *Agent) listenAndServeDNS() error {
	notif := make(chan net.Addr, len(a.config.DNSAddrs))
	for _, addr := range a.config.DNSAddrs {
		// create server
		s, err := NewDNSServer(a)
		if err != nil {
			return err
		}
		a.dnsServers = append(a.dnsServers, s)

		// start server
		a.wgServers.Add(1)
		go func(addr net.Addr) {
			defer a.wgServers.Done()
			err := s.ListenAndServe(addr.Network(), addr.String(), func() { notif <- addr })
			if err != nil && !strings.Contains(err.Error(), "accept") {
				a.logger.Printf("[ERR] agent: Error starting DNS server %s (%s): %v", addr.String(), addr.Network(), err)
			}
		}(addr)
	}

	// wait for servers to be up
	timeout := time.After(time.Second)
	for range a.config.DNSAddrs {
		select {
		case addr := <-notif:
			a.logger.Printf("[INFO] agent: Started DNS server %s (%s)", addr.String(), addr.Network())
			continue
		case <-timeout:
			return fmt.Errorf("agent: timeout starting DNS servers")
		}
	}
	return nil
}

// listenHTTP binds listeners to the provided addresses and also returns
// pre-configured HTTP servers which are not yet started. The motivation is
// that in the current startup/shutdown setup we de-couple the listener
// creation from the server startup assuming that if any of the listeners
// cannot be bound we fail immediately and later failures do not occur.
// Therefore, starting a server with a running listener is assumed to not
// produce an error.
//
// The second motivation is that an HTTPS server needs to use the same TLSConfig
// on both the listener and the HTTP server. When listeners and servers are
// created at different times this becomes difficult to handle without keeping
// the TLS configuration somewhere or recreating it.
//
// This approach should ultimately be refactored to the point where we just
// start the server and any error should trigger a proper shutdown of the agent.
func (a *Agent) listenHTTP() ([]net.Listener, error) {
	var ln []net.Listener

	start := func(proto string, addrs []net.Addr) error {
		for _, addr := range addrs {
			var l net.Listener
			var err error

			switch x := addr.(type) {
			case *net.UnixAddr:
				l, err = a.listenSocket(x.Name)
				if err != nil {
					return err
				}

			case *net.TCPAddr:
				l, err = net.Listen("tcp", x.String())
				if err != nil {
					return err
				}

				l = &tcpKeepAliveListener{l.(*net.TCPListener)}

				if proto == "https" {
					tlscfg, err := a.config.IncomingHTTPSConfig()
					if err != nil {
						return err
					}
					l = tls.NewListener(l, tlscfg)
				}

			default:
				return fmt.Errorf("unsupported address type %T", addr)
			}
			ln = append(ln, l)
		}
		return nil
	}

	if err := start("http", a.config.HTTPAddrs); err != nil {
		for _, l := range ln {
			l.Close()
		}
		return nil, err
	}
	if err := start("https", a.config.HTTPSAddrs); err != nil {
		for _, l := range ln {
			l.Close()
		}
		return nil, err
	}
	return ln, nil
}

// tcpKeepAliveListener sets TCP keep-alive timeouts on accepted
// connections. It's used by NewHttpServer so dead TCP connections
// eventually go away.
type tcpKeepAliveListener struct {
	*net.TCPListener
}

func (ln tcpKeepAliveListener) Accept() (c net.Conn, err error) {
	tc, err := ln.AcceptTCP()
	if err != nil {
		return
	}
	tc.SetKeepAlive(true)
	tc.SetKeepAlivePeriod(30 * time.Second)
	return tc, nil
}

func (a *Agent) listenSocket(path string) (net.Listener, error) {
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		a.logger.Printf("[WARN] agent: Replacing socket %q", path)
	}
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return nil, fmt.Errorf("error removing socket file: %s", err)
	}
	l, err := net.Listen("unix", path)
	if err != nil {
		return nil, err
	}
	user, group, mode := a.config.UnixSocketUser, a.config.UnixSocketGroup, a.config.UnixSocketMode
	if err := setFilePermissions(path, user, group, mode); err != nil {
		return nil, fmt.Errorf("Failed setting up socket: %s", err)
	}
	return l, nil
}

func (a *Agent) serveHTTP(l net.Listener, srv *HTTPServer) error {
	// https://github.com/golang/go/issues/20239
	//
	// In go.8.1 there is a race between Serve and Shutdown. If
	// Shutdown is called before the Serve go routine was scheduled then
	// the Serve go routine never returns. This deadlocks the agent
	// shutdown for some tests since it will wait forever.
	//
	// Since we need to check for an unexported type (*tls.listener)
	// we cannot just perform a type check since the compiler won't let
	// us. We might be able to use reflection but the fmt.Sprintf() hack
	// works just as well.
	srv.proto = "http"
	if strings.Contains("*tls.listener", fmt.Sprintf("%T", l)) {
		srv.proto = "https"
	}
	notif := make(chan net.Addr)
	a.wgServers.Add(1)
	go func() {
		defer a.wgServers.Done()
		notif <- l.Addr()
		err := srv.Serve(l)
		if err != nil && err != http.ErrServerClosed {
			a.logger.Print(err)
		}
	}()

	select {
	case addr := <-notif:
		if srv.proto == "https" {
			a.logger.Printf("[INFO] agent: Started HTTPS server on %s (%s)", addr.String(), addr.Network())
		} else {
			a.logger.Printf("[INFO] agent: Started HTTP server on %s (%s)", addr.String(), addr.Network())
		}
		return nil
	case <-time.After(time.Second):
		return fmt.Errorf("agent: timeout starting HTTP servers")
	}
}

// reloadWatches stops any existing watch plans and attempts to load the given
// set of watches.
func (a *Agent) reloadWatches(cfg *config.RuntimeConfig) error {
	// Stop the current watches.
	for _, wp := range a.watchPlans {
		wp.Stop()
	}
	a.watchPlans = nil

	// Return if there are no watches now.
	if len(cfg.Watches) == 0 {
		return nil
	}

	// Watches use the API to talk to this agent, so that must be enabled.
	if len(cfg.HTTPAddrs) == 0 && len(cfg.HTTPSAddrs) == 0 {
		return fmt.Errorf("watch plans require an HTTP or HTTPS endpoint")
	}

	// Compile the watches
	var watchPlans []*watch.Plan
	for _, params := range cfg.Watches {
		// Parse the watches, excluding the handler
		wp, err := watch.ParseExempt(params, []string{"handler", "args"})
		if err != nil {
			return fmt.Errorf("Failed to parse watch (%#v): %v", params, err)
		}

		// Get the handler and subprocess arguments
		handler, hasHandler := wp.Exempt["handler"]
		args, hasArgs := wp.Exempt["args"]
		if hasHandler {
			a.logger.Printf("[WARN] agent: The 'handler' field in watches has been deprecated " +
				"and replaced with the 'args' field. See https://www.consul.io/docs/agent/watches.html")
		}
		if _, ok := handler.(string); hasHandler && !ok {
			return fmt.Errorf("Watch handler must be a string")
		}
		if raw, ok := args.([]interface{}); hasArgs && ok {
			var parsed []string
			for _, arg := range raw {
				if v, ok := arg.(string); !ok {
					return fmt.Errorf("Watch args must be a list of strings")
				} else {
					parsed = append(parsed, v)
				}
			}
			wp.Exempt["args"] = parsed
		} else if hasArgs && !ok {
			return fmt.Errorf("Watch args must be a list of strings")
		}
		if hasHandler && hasArgs {
			return fmt.Errorf("Cannot define both watch handler and args")
		}
		if !hasHandler && !hasArgs {
			return fmt.Errorf("Must define either watch handler or args")
		}

		// Store the watch plan
		watchPlans = append(watchPlans, wp)
	}

	// Determine the primary http(s) endpoint.
	var netaddr net.Addr
	if len(cfg.HTTPAddrs) > 0 {
		netaddr = cfg.HTTPAddrs[0]
	} else {
		netaddr = cfg.HTTPSAddrs[0]
	}
	addr := netaddr.String()
	if netaddr.Network() == "unix" {
		addr = "unix://" + addr
	}

	// Fire off a goroutine for each new watch plan.
	for _, wp := range watchPlans {
		a.watchPlans = append(a.watchPlans, wp)
		go func(wp *watch.Plan) {
			var handler interface{}
			if h, ok := wp.Exempt["handler"]; ok {
				handler = h
			} else {
				handler = wp.Exempt["args"]
			}
			wp.Handler = makeWatchHandler(a.LogOutput, handler)
			wp.LogOutput = a.LogOutput
			if err := wp.Run(addr); err != nil {
				a.logger.Printf("[ERR] Failed to run watch: %v", err)
			}
		}(wp)
	}
	return nil
}

// consulConfig is used to return a consul configuration
func (a *Agent) consulConfig() (*consul.Config, error) {
	// Start with the provided config or default config
	base := consul.DefaultConfig()

	// This is set when the agent starts up
	base.NodeID = a.config.NodeID

	// Apply dev mode
	base.DevMode = a.config.DevMode

	// Override with our config
	// todo(fs): these are now always set in the runtime config so we can simplify this
	// todo(fs): or is there a reason to keep it like that?
	base.Datacenter = a.config.Datacenter
	base.DataDir = a.config.DataDir
	base.NodeName = a.config.NodeName

	base.CoordinateUpdateBatchSize = a.config.ConsulCoordinateUpdateBatchSize
	base.CoordinateUpdateMaxBatches = a.config.ConsulCoordinateUpdateMaxBatches
	base.CoordinateUpdatePeriod = a.config.ConsulCoordinateUpdatePeriod

	base.RaftConfig.HeartbeatTimeout = a.config.ConsulRaftHeartbeatTimeout
	base.RaftConfig.LeaderLeaseTimeout = a.config.ConsulRaftLeaderLeaseTimeout
	base.RaftConfig.ElectionTimeout = a.config.ConsulRaftElectionTimeout

	base.SerfLANConfig.MemberlistConfig.BindAddr = a.config.SerfBindAddrLAN.IP.String()
	base.SerfLANConfig.MemberlistConfig.BindPort = a.config.SerfBindAddrLAN.Port
	base.SerfLANConfig.MemberlistConfig.AdvertiseAddr = a.config.SerfAdvertiseAddrLAN.IP.String()
	base.SerfLANConfig.MemberlistConfig.AdvertisePort = a.config.SerfAdvertiseAddrLAN.Port
	base.SerfLANConfig.MemberlistConfig.GossipVerifyIncoming = a.config.EncryptVerifyIncoming
	base.SerfLANConfig.MemberlistConfig.GossipVerifyOutgoing = a.config.EncryptVerifyOutgoing
	base.SerfLANConfig.MemberlistConfig.GossipInterval = a.config.ConsulSerfLANGossipInterval
	base.SerfLANConfig.MemberlistConfig.ProbeInterval = a.config.ConsulSerfLANProbeInterval
	base.SerfLANConfig.MemberlistConfig.ProbeTimeout = a.config.ConsulSerfLANProbeTimeout
	base.SerfLANConfig.MemberlistConfig.SuspicionMult = a.config.ConsulSerfLANSuspicionMult

	base.SerfWANConfig.MemberlistConfig.BindAddr = a.config.SerfBindAddrWAN.IP.String()
	base.SerfWANConfig.MemberlistConfig.BindPort = a.config.SerfBindAddrWAN.Port
	base.SerfWANConfig.MemberlistConfig.AdvertiseAddr = a.config.SerfAdvertiseAddrWAN.IP.String()
	base.SerfWANConfig.MemberlistConfig.AdvertisePort = a.config.SerfAdvertiseAddrWAN.Port
	base.SerfWANConfig.MemberlistConfig.GossipVerifyIncoming = a.config.EncryptVerifyIncoming
	base.SerfWANConfig.MemberlistConfig.GossipVerifyOutgoing = a.config.EncryptVerifyOutgoing
	base.SerfWANConfig.MemberlistConfig.GossipInterval = a.config.ConsulSerfWANGossipInterval
	base.SerfWANConfig.MemberlistConfig.ProbeInterval = a.config.ConsulSerfWANProbeInterval
	base.SerfWANConfig.MemberlistConfig.ProbeTimeout = a.config.ConsulSerfWANProbeTimeout
	base.SerfWANConfig.MemberlistConfig.SuspicionMult = a.config.ConsulSerfWANSuspicionMult

	base.RPCAddr = a.config.RPCBindAddr
	base.RPCAdvertise = a.config.RPCAdvertiseAddr

	if a.config.ReconnectTimeoutLAN != 0 {
		base.SerfLANConfig.ReconnectTimeout = a.config.ReconnectTimeoutLAN
	}
	if a.config.ReconnectTimeoutWAN != 0 {
		base.SerfWANConfig.ReconnectTimeout = a.config.ReconnectTimeoutWAN
	}

	base.Segment = a.config.SegmentName
	if len(a.config.Segments) > 0 {
		segments, err := a.segmentConfig()
		if err != nil {
			return nil, err
		}
		base.Segments = segments
	}
	if a.config.Bootstrap {
		base.Bootstrap = true
	}
	if a.config.RejoinAfterLeave {
		base.RejoinAfterLeave = true
	}
	if a.config.BootstrapExpect != 0 {
		base.BootstrapExpect = a.config.BootstrapExpect
	}
	if a.config.RPCProtocol > 0 {
		base.ProtocolVersion = uint8(a.config.RPCProtocol)
	}
	if a.config.RaftProtocol != 0 {
		base.RaftConfig.ProtocolVersion = raft.ProtocolVersion(a.config.RaftProtocol)
	}
	if a.config.ACLMasterToken != "" {
		base.ACLMasterToken = a.config.ACLMasterToken
	}
	if a.config.ACLDatacenter != "" {
		base.ACLDatacenter = a.config.ACLDatacenter
	}
	if a.config.ACLTTL != 0 {
		base.ACLTTL = a.config.ACLTTL
	}
	if a.config.ACLDefaultPolicy != "" {
		base.ACLDefaultPolicy = a.config.ACLDefaultPolicy
	}
	if a.config.ACLDownPolicy != "" {
		base.ACLDownPolicy = a.config.ACLDownPolicy
	}
	base.EnableACLReplication = a.config.EnableACLReplication
	if a.config.ACLEnforceVersion8 {
		base.ACLEnforceVersion8 = a.config.ACLEnforceVersion8
	}
	if a.config.ACLEnableKeyListPolicy {
		base.ACLEnableKeyListPolicy = a.config.ACLEnableKeyListPolicy
	}
	if a.config.SessionTTLMin != 0 {
		base.SessionTTLMin = a.config.SessionTTLMin
	}
	if a.config.AutopilotCleanupDeadServers {
		base.AutopilotConfig.CleanupDeadServers = a.config.AutopilotCleanupDeadServers
	}
	if a.config.AutopilotLastContactThreshold != 0 {
		base.AutopilotConfig.LastContactThreshold = a.config.AutopilotLastContactThreshold
	}
	if a.config.AutopilotMaxTrailingLogs != 0 {
		base.AutopilotConfig.MaxTrailingLogs = uint64(a.config.AutopilotMaxTrailingLogs)
	}
	if a.config.AutopilotServerStabilizationTime != 0 {
		base.AutopilotConfig.ServerStabilizationTime = a.config.AutopilotServerStabilizationTime
	}
	if a.config.NonVotingServer {
		base.NonVoter = a.config.NonVotingServer
	}
	if a.config.AutopilotRedundancyZoneTag != "" {
		base.AutopilotConfig.RedundancyZoneTag = a.config.AutopilotRedundancyZoneTag
	}
	if a.config.AutopilotDisableUpgradeMigration {
		base.AutopilotConfig.DisableUpgradeMigration = a.config.AutopilotDisableUpgradeMigration
	}
	if a.config.AutopilotUpgradeVersionTag != "" {
		base.AutopilotConfig.UpgradeVersionTag = a.config.AutopilotUpgradeVersionTag
	}

	// make sure the advertise address is always set
	if base.RPCAdvertise == nil {
		base.RPCAdvertise = base.RPCAddr
	}

	// Rate limiting for RPC calls.
	if a.config.RPCRateLimit > 0 {
		base.RPCRate = a.config.RPCRateLimit
	}
	if a.config.RPCMaxBurst > 0 {
		base.RPCMaxBurst = a.config.RPCMaxBurst
	}

	// RPC-related performance configs.
	if a.config.RPCHoldTimeout > 0 {
		base.RPCHoldTimeout = a.config.RPCHoldTimeout
	}
	if a.config.LeaveDrainTime > 0 {
		base.LeaveDrainTime = a.config.LeaveDrainTime
	}

	// set the src address for outgoing rpc connections
	// Use port 0 so that outgoing connections use a random port.
	if !ipaddr.IsAny(base.RPCAddr.IP) {
		base.RPCSrcAddr = &net.TCPAddr{IP: base.RPCAddr.IP}
	}

	// Format the build string
	revision := a.config.Revision
	if len(revision) > 8 {
		revision = revision[:8]
	}
	base.Build = fmt.Sprintf("%s%s:%s", a.config.Version, a.config.VersionPrerelease, revision)

	// Copy the TLS configuration
	base.VerifyIncoming = a.config.VerifyIncoming || a.config.VerifyIncomingRPC
	if a.config.CAPath != "" || a.config.CAFile != "" {
		base.UseTLS = true
	}
	base.VerifyOutgoing = a.config.VerifyOutgoing
	base.VerifyServerHostname = a.config.VerifyServerHostname
	base.CAFile = a.config.CAFile
	base.CAPath = a.config.CAPath
	base.CertFile = a.config.CertFile
	base.KeyFile = a.config.KeyFile
	base.ServerName = a.config.ServerName
	base.Domain = a.config.DNSDomain
	base.TLSMinVersion = a.config.TLSMinVersion
	base.TLSCipherSuites = a.config.TLSCipherSuites
	base.TLSPreferServerCipherSuites = a.config.TLSPreferServerCipherSuites

	// Setup the user event callback
	base.UserEventHandler = func(e serf.UserEvent) {
		select {
		case a.eventCh <- e:
		case <-a.shutdownCh:
		}
	}

	// Setup the loggers
	base.LogOutput = a.LogOutput

	// This will set up the LAN keyring, as well as the WAN and any segments
	// for servers.
	if err := a.setupKeyrings(base); err != nil {
		return nil, fmt.Errorf("Failed to configure keyring: %v", err)
	}

	return base, nil
}

// Setup the serf and memberlist config for any defined network segments.
func (a *Agent) segmentConfig() ([]consul.NetworkSegment, error) {
	var segments []consul.NetworkSegment
	config := a.config

	for _, s := range config.Segments {
		serfConf := consul.DefaultConfig().SerfLANConfig

		serfConf.MemberlistConfig.BindAddr = s.Bind.IP.String()
		serfConf.MemberlistConfig.BindPort = s.Bind.Port
		serfConf.MemberlistConfig.AdvertiseAddr = s.Advertise.IP.String()
		serfConf.MemberlistConfig.AdvertisePort = s.Advertise.Port

		if config.ReconnectTimeoutLAN != 0 {
			serfConf.ReconnectTimeout = config.ReconnectTimeoutLAN
		}
		if config.EncryptVerifyIncoming {
			serfConf.MemberlistConfig.GossipVerifyIncoming = config.EncryptVerifyIncoming
		}
		if config.EncryptVerifyOutgoing {
			serfConf.MemberlistConfig.GossipVerifyOutgoing = config.EncryptVerifyOutgoing
		}

		var rpcAddr *net.TCPAddr
		if s.RPCListener {
			rpcAddr = &net.TCPAddr{
				IP:   s.Bind.IP,
				Port: a.config.ServerPort,
			}
		}

		segments = append(segments, consul.NetworkSegment{
			Name:       s.Name,
			Bind:       serfConf.MemberlistConfig.BindAddr,
			Advertise:  serfConf.MemberlistConfig.AdvertiseAddr,
			Port:       s.Bind.Port,
			RPCAddr:    rpcAddr,
			SerfConfig: serfConf,
		})
	}

	return segments, nil
}

// makeRandomID will generate a random UUID for a node.
func (a *Agent) makeRandomID() (string, error) {
	id, err := uuid.GenerateUUID()
	if err != nil {
		return "", err
	}

	a.logger.Printf("[DEBUG] Using random ID %q as node ID", id)
	return id, nil
}

// makeNodeID will try to find a host-specific ID, or else will generate a
// random ID. The returned ID will always be formatted as a GUID. We don't tell
// the caller whether this ID is random or stable since the consequences are
// high for us if this changes, so we will persist it either way. This will let
// gopsutil change implementations without affecting in-place upgrades of nodes.
func (a *Agent) makeNodeID() (string, error) {
	// If they've disabled host-based IDs then just make a random one.
	if a.config.DisableHostNodeID {
		return a.makeRandomID()
	}

	// Try to get a stable ID associated with the host itself.
	info, err := host.Info()
	if err != nil {
		a.logger.Printf("[DEBUG] Couldn't get a unique ID from the host: %v", err)
		return a.makeRandomID()
	}

	// Make sure the host ID parses as a UUID, since we don't have complete
	// control over this process.
	id := strings.ToLower(info.HostID)
	if _, err := uuid.ParseUUID(id); err != nil {
		a.logger.Printf("[DEBUG] Unique ID %q from host isn't formatted as a UUID: %v",
			id, err)
		return a.makeRandomID()
	}

	// Hash the input to make it well distributed. The reported Host UUID may be
	// similar across nodes if they are on a cloud provider or on motherboards
	// created from the same batch.
	buf := sha512.Sum512([]byte(id))
	id = fmt.Sprintf("%08x-%04x-%04x-%04x-%12x",
		buf[0:4],
		buf[4:6],
		buf[6:8],
		buf[8:10],
		buf[10:16])

	a.logger.Printf("[DEBUG] Using unique ID %q from host as node ID", id)
	return id, nil
}

// setupNodeID will pull the persisted node ID, if any, or create a random one
// and persist it.
func (a *Agent) setupNodeID(config *config.RuntimeConfig) error {
	// If they've configured a node ID manually then just use that, as
	// long as it's valid.
	if config.NodeID != "" {
		config.NodeID = types.NodeID(strings.ToLower(string(config.NodeID)))
		if _, err := uuid.ParseUUID(string(config.NodeID)); err != nil {
			return err
		}

		return nil
	}

	// For dev mode we have no filesystem access so just make one.
	if a.config.DevMode {
		id, err := a.makeNodeID()
		if err != nil {
			return err
		}

		config.NodeID = types.NodeID(id)
		return nil
	}

	// Load saved state, if any. Since a user could edit this, we also
	// validate it.
	fileID := filepath.Join(config.DataDir, "node-id")
	if _, err := os.Stat(fileID); err == nil {
		rawID, err := ioutil.ReadFile(fileID)
		if err != nil {
			return err
		}

		nodeID := strings.TrimSpace(string(rawID))
		nodeID = strings.ToLower(nodeID)
		if _, err := uuid.ParseUUID(nodeID); err != nil {
			return err
		}

		config.NodeID = types.NodeID(nodeID)
	}

	// If we still don't have a valid node ID, make one.
	if config.NodeID == "" {
		id, err := a.makeNodeID()
		if err != nil {
			return err
		}
		if err := lib.EnsurePath(fileID, false); err != nil {
			return err
		}
		if err := ioutil.WriteFile(fileID, []byte(id), 0600); err != nil {
			return err
		}

		config.NodeID = types.NodeID(id)
	}
	return nil
}

// setupBaseKeyrings configures the LAN and WAN keyrings.
func (a *Agent) setupBaseKeyrings(config *consul.Config) error {
	// If the keyring file is disabled then just poke the provided key
	// into the in-memory keyring.
	if a.config.DisableKeyringFile {
		if a.config.EncryptKey == "" {
			return nil
		}

		keys := []string{a.config.EncryptKey}
		if err := loadKeyring(config.SerfLANConfig, keys); err != nil {
			return err
		}
		if a.config.ServerMode {
			if err := loadKeyring(config.SerfWANConfig, keys); err != nil {
				return err
			}
		}
		return nil
	}

	// Otherwise, we need to deal with the keyring files.
	fileLAN := filepath.Join(a.config.DataDir, SerfLANKeyring)
	fileWAN := filepath.Join(a.config.DataDir, SerfWANKeyring)

	if a.config.EncryptKey == "" {
		goto LOAD
	}
	if _, err := os.Stat(fileLAN); err != nil {
		if err := initKeyring(fileLAN, a.config.EncryptKey); err != nil {
			return err
		}
	}
	if a.config.ServerMode {
		if _, err := os.Stat(fileWAN); err != nil {
			if err := initKeyring(fileWAN, a.config.EncryptKey); err != nil {
				return err
			}
		}
	}

LOAD:
	if _, err := os.Stat(fileLAN); err == nil {
		config.SerfLANConfig.KeyringFile = fileLAN
	}
	if err := loadKeyringFile(config.SerfLANConfig); err != nil {
		return err
	}
	if a.config.ServerMode {
		if _, err := os.Stat(fileWAN); err == nil {
			config.SerfWANConfig.KeyringFile = fileWAN
		}
		if err := loadKeyringFile(config.SerfWANConfig); err != nil {
			return err
		}
	}

	return nil
}

// setupKeyrings is used to initialize and load keyrings during agent startup.
func (a *Agent) setupKeyrings(config *consul.Config) error {
	// First set up the LAN and WAN keyrings.
	if err := a.setupBaseKeyrings(config); err != nil {
		return err
	}

	// If there's no LAN keyring then there's nothing else to set up for
	// any segments.
	lanKeyring := config.SerfLANConfig.MemberlistConfig.Keyring
	if lanKeyring == nil {
		return nil
	}

	// Copy the initial state of the LAN keyring into each segment config.
	// Segments don't have their own keyring file, they rely on the LAN
	// holding the state so things can't get out of sync.
	k, pk := lanKeyring.GetKeys(), lanKeyring.GetPrimaryKey()
	for _, segment := range config.Segments {
		keyring, err := memberlist.NewKeyring(k, pk)
		if err != nil {
			return err
		}
		segment.SerfConfig.MemberlistConfig.Keyring = keyring
	}
	return nil
}

// registerEndpoint registers a handler for the consul RPC server
// under a unique name while making it accessible under the provided
// name. This allows overwriting handlers for the golang net/rpc
// service which does not allow this.
func (a *Agent) registerEndpoint(name string, handler interface{}) error {
	srv, ok := a.delegate.(*consul.Server)
	if !ok {
		panic("agent must be a server")
	}
	realname := fmt.Sprintf("%s-%d", name, time.Now().UnixNano())
	a.endpointsLock.Lock()
	a.endpoints[name] = realname
	a.endpointsLock.Unlock()
	return srv.RegisterEndpoint(realname, handler)
}

// RPC is used to make an RPC call to the Consul servers
// This allows the agent to implement the Consul.Interface
func (a *Agent) RPC(method string, args interface{}, reply interface{}) error {
	a.endpointsLock.RLock()
	// fast path: only translate if there are overrides
	if len(a.endpoints) > 0 {
		p := strings.SplitN(method, ".", 2)
		if e := a.endpoints[p[0]]; e != "" {
			method = e + "." + p[1]
		}
	}
	a.endpointsLock.RUnlock()
	return a.delegate.RPC(method, args, reply)
}

// SnapshotRPC performs the requested snapshot RPC against the Consul server in
// a streaming manner. The contents of in will be read and passed along as the
// payload, and the response message will determine the error status, and any
// return payload will be written to out.
func (a *Agent) SnapshotRPC(args *structs.SnapshotRequest, in io.Reader, out io.Writer,
	replyFn structs.SnapshotReplyFn) error {
	return a.delegate.SnapshotRPC(args, in, out, replyFn)
}

// Leave is used to prepare the agent for a graceful shutdown
func (a *Agent) Leave() error {
	return a.delegate.Leave()
}

// ShutdownAgent is used to hard stop the agent. Should be preceded by
// Leave to do it gracefully. Should be followed by ShutdownEndpoints to
// terminate the HTTP and DNS servers as well.
func (a *Agent) ShutdownAgent() error {
	a.shutdownLock.Lock()
	defer a.shutdownLock.Unlock()

	if a.shutdown {
		return nil
	}
	a.logger.Println("[INFO] agent: Requesting shutdown")

	// Stop all the checks
	a.checkLock.Lock()
	defer a.checkLock.Unlock()
	for _, chk := range a.checkMonitors {
		chk.Stop()
	}
	for _, chk := range a.checkTTLs {
		chk.Stop()
	}
	for _, chk := range a.checkHTTPs {
		chk.Stop()
	}
	for _, chk := range a.checkTCPs {
		chk.Stop()
	}
	for _, chk := range a.checkDockers {
		chk.Stop()
	}

	var err error
	if a.delegate != nil {
		err = a.delegate.Shutdown()
		if _, ok := a.delegate.(*consul.Server); ok {
			a.logger.Print("[INFO] agent: consul server down")
		} else {
			a.logger.Print("[INFO] agent: consul client down")
		}
	}

	pidErr := a.deletePid()
	if pidErr != nil {
		a.logger.Println("[WARN] agent: could not delete pid file ", pidErr)
	}

	a.logger.Println("[INFO] agent: shutdown complete")
	a.shutdown = true
	close(a.shutdownCh)
	return err
}

// ShutdownEndpoints terminates the HTTP and DNS servers. Should be
// preceeded by ShutdownAgent.
func (a *Agent) ShutdownEndpoints() {
	a.shutdownLock.Lock()
	defer a.shutdownLock.Unlock()

	if len(a.dnsServers) == 0 || len(a.httpServers) == 0 {
		return
	}

	for _, srv := range a.dnsServers {
		a.logger.Printf("[INFO] agent: Stopping DNS server %s (%s)", srv.Server.Addr, srv.Server.Net)
		srv.Shutdown()
	}
	a.dnsServers = nil

	for _, srv := range a.httpServers {
		a.logger.Printf("[INFO] agent: Stopping %s server %s (%s)", strings.ToUpper(srv.proto), srv.addr.String(), srv.addr.Network())
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		srv.Shutdown(ctx)
		if ctx.Err() == context.DeadlineExceeded {
			a.logger.Printf("[WARN] agent: Timeout stopping %s server %s (%s)", strings.ToUpper(srv.proto), srv.addr.String(), srv.addr.Network())
		}
	}
	a.httpServers = nil

	a.logger.Println("[INFO] agent: Waiting for endpoints to shut down")
	a.wgServers.Wait()
	a.logger.Print("[INFO] agent: Endpoints down")
}

// ReloadCh is used to return a channel that can be
// used for triggering reloads and returning a response.
func (a *Agent) ReloadCh() chan chan error {
	return a.reloadCh
}

// RetryJoinCh is a channel that transports errors
// from the retry join process.
func (a *Agent) RetryJoinCh() <-chan error {
	return a.retryJoinCh
}

// ShutdownCh is used to return a channel that can be
// selected to wait for the agent to perform a shutdown.
func (a *Agent) ShutdownCh() <-chan struct{} {
	return a.shutdownCh
}

// JoinLAN is used to have the agent join a LAN cluster
func (a *Agent) JoinLAN(addrs []string) (n int, err error) {
	a.logger.Printf("[INFO] agent: (LAN) joining: %v", addrs)
	n, err = a.delegate.JoinLAN(addrs)
	a.logger.Printf("[INFO] agent: (LAN) joined: %d Err: %v", n, err)
	if err == nil && a.joinLANNotifier != nil {
		if notifErr := a.joinLANNotifier.Notify(systemd.Ready); notifErr != nil {
			a.logger.Printf("[DEBUG] agent: systemd notify failed: %v", notifErr)
		}
	}
	return
}

// JoinWAN is used to have the agent join a WAN cluster
func (a *Agent) JoinWAN(addrs []string) (n int, err error) {
	a.logger.Printf("[INFO] agent: (WAN) joining: %v", addrs)
	if srv, ok := a.delegate.(*consul.Server); ok {
		n, err = srv.JoinWAN(addrs)
	} else {
		err = fmt.Errorf("Must be a server to join WAN cluster")
	}
	a.logger.Printf("[INFO] agent: (WAN) joined: %d Err: %v", n, err)
	return
}

// ForceLeave is used to remove a failed node from the cluster
func (a *Agent) ForceLeave(node string) (err error) {
	a.logger.Printf("[INFO] Force leaving node: %v", node)
	err = a.delegate.RemoveFailedNode(node)
	if err != nil {
		a.logger.Printf("[WARN] Failed to remove node: %v", err)
	}
	return err
}

// LocalMember is used to return the local node
func (a *Agent) LocalMember() serf.Member {
	return a.delegate.LocalMember()
}

// LANMembers is used to retrieve the LAN members
func (a *Agent) LANMembers() []serf.Member {
	return a.delegate.LANMembers()
}

// WANMembers is used to retrieve the WAN members
func (a *Agent) WANMembers() []serf.Member {
	if srv, ok := a.delegate.(*consul.Server); ok {
		return srv.WANMembers()
	}
	return nil
}

// StartSync is called once Services and Checks are registered.
// This is called to prevent a race between clients and the anti-entropy routines
func (a *Agent) StartSync() {
	// Start the anti entropy routine
	go a.state.antiEntropy(a.shutdownCh)
}

// PauseSync is used to pause anti-entropy while bulk changes are make
func (a *Agent) PauseSync() {
	a.state.Pause()
}

// ResumeSync is used to unpause anti-entropy after bulk changes are make
func (a *Agent) ResumeSync() {
	a.state.Resume()
}

// GetLANCoordinate returns the coordinates of this node in the local pools
// (assumes coordinates are enabled, so check that before calling).
func (a *Agent) GetLANCoordinate() (lib.CoordinateSet, error) {
	return a.delegate.GetLANCoordinate()
}

// sendCoordinate is a long-running loop that periodically sends our coordinate
// to the server. Closing the agent's shutdownChannel will cause this to exit.
func (a *Agent) sendCoordinate() {
OUTER:
	for {
		rate := a.config.SyncCoordinateRateTarget
		min := a.config.SyncCoordinateIntervalMin
		intv := lib.RateScaledInterval(rate, min, len(a.LANMembers()))
		intv = intv + lib.RandomStagger(intv)

		select {
		case <-time.After(intv):
			members := a.LANMembers()
			grok, err := consul.CanServersUnderstandProtocol(members, 3)
			if err != nil {
				a.logger.Printf("[ERR] agent: Failed to check servers: %s", err)
				continue
			}
			if !grok {
				a.logger.Printf("[DEBUG] agent: Skipping coordinate updates until servers are upgraded")
				continue
			}

			cs, err := a.GetLANCoordinate()
			if err != nil {
				a.logger.Printf("[ERR] agent: Failed to get coordinate: %s", err)
				continue
			}

			for segment, coord := range cs {
				req := structs.CoordinateUpdateRequest{
					Datacenter:   a.config.Datacenter,
					Node:         a.config.NodeName,
					Segment:      segment,
					Coord:        coord,
					WriteRequest: structs.WriteRequest{Token: a.tokens.AgentToken()},
				}
				var reply struct{}
				if err := a.RPC("Coordinate.Update", &req, &reply); err != nil {
					if acl.IsErrPermissionDenied(err) {
						a.logger.Printf("[WARN] agent: Coordinate update blocked by ACLs")
					} else {
						a.logger.Printf("[ERR] agent: Coordinate update error: %v", err)
					}
					continue OUTER
				}
			}
		case <-a.shutdownCh:
			return
		}
	}
}

// reapServicesInternal does a single pass, looking for services to reap.
func (a *Agent) reapServicesInternal() {
	reaped := make(map[string]struct{})
	for checkID, check := range a.state.CriticalChecks() {
		// There's nothing to do if there's no service.
		if check.Check.ServiceID == "" {
			continue
		}

		// There might be multiple checks for one service, so
		// we don't need to reap multiple times.
		serviceID := check.Check.ServiceID
		if _, ok := reaped[serviceID]; ok {
			continue
		}

		// See if there's a timeout.
		a.checkLock.Lock()
		timeout, ok := a.checkReapAfter[checkID]
		a.checkLock.Unlock()

		// Reap, if necessary. We keep track of which service
		// this is so that we won't try to remove it again.
		if ok && check.CriticalFor > timeout {
			reaped[serviceID] = struct{}{}
			a.RemoveService(serviceID, true)
			a.logger.Printf("[INFO] agent: Check %q for service %q has been critical for too long; deregistered service",
				checkID, serviceID)
		}
	}
}

// reapServices is a long running goroutine that looks for checks that have been
// critical too long and dregisters their associated services.
func (a *Agent) reapServices() {
	for {
		select {
		case <-time.After(a.config.CheckReapInterval):
			a.reapServicesInternal()

		case <-a.shutdownCh:
			return
		}
	}

}

// persistedService is used to wrap a service definition and bundle it
// with an ACL token so we can restore both at a later agent start.
type persistedService struct {
	Token   string
	Service *structs.NodeService
}

// persistService saves a service definition to a JSON file in the data dir
func (a *Agent) persistService(service *structs.NodeService) error {
	svcPath := filepath.Join(a.config.DataDir, servicesDir, stringHash(service.ID))

	wrapped := persistedService{
		Token:   a.state.ServiceToken(service.ID),
		Service: service,
	}
	encoded, err := json.Marshal(wrapped)
	if err != nil {
		return err
	}

	return writeFileAtomic(svcPath, encoded)
}

// purgeService removes a persisted service definition file from the data dir
func (a *Agent) purgeService(serviceID string) error {
	svcPath := filepath.Join(a.config.DataDir, servicesDir, stringHash(serviceID))
	if _, err := os.Stat(svcPath); err == nil {
		return os.Remove(svcPath)
	}
	return nil
}

// persistCheck saves a check definition to the local agent's state directory
func (a *Agent) persistCheck(check *structs.HealthCheck, chkType *structs.CheckType) error {
	checkPath := filepath.Join(a.config.DataDir, checksDir, checkIDHash(check.CheckID))

	// Create the persisted check
	wrapped := persistedCheck{
		Check:   check,
		ChkType: chkType,
		Token:   a.state.CheckToken(check.CheckID),
	}

	encoded, err := json.Marshal(wrapped)
	if err != nil {
		return err
	}

	return writeFileAtomic(checkPath, encoded)
}

// purgeCheck removes a persisted check definition file from the data dir
func (a *Agent) purgeCheck(checkID types.CheckID) error {
	checkPath := filepath.Join(a.config.DataDir, checksDir, checkIDHash(checkID))
	if _, err := os.Stat(checkPath); err == nil {
		return os.Remove(checkPath)
	}
	return nil
}

// writeFileAtomic writes the given contents to a temporary file in the same
// directory, does an fsync and then renames the file to its real path
func writeFileAtomic(path string, contents []byte) error {
	uuid, err := uuid.GenerateUUID()
	if err != nil {
		return err
	}
	tempPath := fmt.Sprintf("%s-%s.tmp", path, uuid)

	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	fh, err := os.OpenFile(tempPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0600)
	if err != nil {
		return err
	}
	if _, err := fh.Write(contents); err != nil {
		fh.Close()
		os.Remove(tempPath)
		return err
	}
	if err := fh.Sync(); err != nil {
		fh.Close()
		os.Remove(tempPath)
		return err
	}
	if err := fh.Close(); err != nil {
		os.Remove(tempPath)
		return err
	}
	if err := os.Rename(tempPath, path); err != nil {
		os.Remove(tempPath)
		return err
	}
	return nil
}

// AddService is used to add a service entry.
// This entry is persistent and the agent will make a best effort to
// ensure it is registered
func (a *Agent) AddService(service *structs.NodeService, chkTypes []*structs.CheckType, persist bool, token string) error {
	if service.Service == "" {
		return fmt.Errorf("Service name missing")
	}
	if service.ID == "" && service.Service != "" {
		service.ID = service.Service
	}
	for _, check := range chkTypes {
		if err := check.Validate(); err != nil {
			return fmt.Errorf("Check is not valid: %v", err)
		}
	}

	// Warn if the service name is incompatible with DNS
	if InvalidDnsRe.MatchString(service.Service) {
		a.logger.Printf("[WARN] Service name %q will not be discoverable "+
			"via DNS due to invalid characters. Valid characters include "+
			"all alpha-numerics and dashes.", service.Service)
	}

	// Warn if any tags are incompatible with DNS
	for _, tag := range service.Tags {
		if InvalidDnsRe.MatchString(tag) {
			a.logger.Printf("[DEBUG] Service tag %q will not be discoverable "+
				"via DNS due to invalid characters. Valid characters include "+
				"all alpha-numerics and dashes.", tag)
		}
	}

	// Pause the service syncs during modification
	a.PauseSync()
	defer a.ResumeSync()

	// Take a snapshot of the current state of checks (if any), and
	// restore them before resuming anti-entropy.
	snap := a.snapshotCheckState()
	defer a.restoreCheckState(snap)

	// Add the service
	a.state.AddService(service, token)

	// Persist the service to a file
	if persist && !a.config.DevMode {
		if err := a.persistService(service); err != nil {
			return err
		}
	}

	// Create an associated health check
	for i, chkType := range chkTypes {
		checkID := string(chkType.CheckID)
		if checkID == "" {
			checkID = fmt.Sprintf("service:%s", service.ID)
			if len(chkTypes) > 1 {
				checkID += fmt.Sprintf(":%d", i+1)
			}
		}
		name := chkType.Name
		if name == "" {
			name = fmt.Sprintf("Service '%s' check", service.Service)
		}
		check := &structs.HealthCheck{
			Node:        a.config.NodeName,
			CheckID:     types.CheckID(checkID),
			Name:        name,
			Status:      api.HealthCritical,
			Notes:       chkType.Notes,
			ServiceID:   service.ID,
			ServiceName: service.Service,
		}
		if chkType.Status != "" {
			check.Status = chkType.Status
		}
		if err := a.AddCheck(check, chkType, persist, token); err != nil {
			return err
		}
	}
	return nil
}

// RemoveService is used to remove a service entry.
// The agent will make a best effort to ensure it is deregistered
func (a *Agent) RemoveService(serviceID string, persist bool) error {
	// Validate ServiceID
	if serviceID == "" {
		return fmt.Errorf("ServiceID missing")
	}

	// Remove service immediately
	if err := a.state.RemoveService(serviceID); err != nil {
		a.logger.Printf("[WARN] agent: Failed to deregister service %q: %s", serviceID, err)
		return nil
	}

	// Remove the service from the data dir
	if persist {
		if err := a.purgeService(serviceID); err != nil {
			return err
		}
	}

	// Deregister any associated health checks
	for checkID, health := range a.state.Checks() {
		if health.ServiceID != serviceID {
			continue
		}
		if err := a.RemoveCheck(checkID, persist); err != nil {
			return err
		}
	}

	log.Printf("[DEBUG] agent: removed service %q", serviceID)
	return nil
}

// AddCheck is used to add a health check to the agent.
// This entry is persistent and the agent will make a best effort to
// ensure it is registered. The Check may include a CheckType which
// is used to automatically update the check status
func (a *Agent) AddCheck(check *structs.HealthCheck, chkType *structs.CheckType, persist bool, token string) error {
	if check.CheckID == "" {
		return fmt.Errorf("CheckID missing")
	}

	if chkType != nil {
		if err := chkType.Validate(); err != nil {
			return fmt.Errorf("Check is not valid: %v", err)
		}

		if chkType.IsScript() && !a.config.EnableScriptChecks {
			return fmt.Errorf("Scripts are disabled on this agent; to enable, configure 'enable_script_checks' to true")
		}
	}

	if check.ServiceID != "" {
		svc, ok := a.state.Services()[check.ServiceID]
		if !ok {
			return fmt.Errorf("ServiceID %q does not exist", check.ServiceID)
		}
		check.ServiceName = svc.Service
	}

	a.checkLock.Lock()
	defer a.checkLock.Unlock()

	// Check if already registered
	if chkType != nil {
		switch {

		case chkType.IsTTL():
			if existing, ok := a.checkTTLs[check.CheckID]; ok {
				existing.Stop()
				delete(a.checkTTLs, check.CheckID)
			}

			ttl := &CheckTTL{
				Notify:  a.state,
				CheckID: check.CheckID,
				TTL:     chkType.TTL,
				Logger:  a.logger,
			}

			// Restore persisted state, if any
			if err := a.loadCheckState(check); err != nil {
				a.logger.Printf("[WARN] agent: failed restoring state for check %q: %s",
					check.CheckID, err)
			}

			ttl.Start()
			a.checkTTLs[check.CheckID] = ttl

		case chkType.IsHTTP():
			if existing, ok := a.checkHTTPs[check.CheckID]; ok {
				existing.Stop()
				delete(a.checkHTTPs, check.CheckID)
			}
			if chkType.Interval < MinInterval {
				a.logger.Println(fmt.Sprintf("[WARN] agent: check '%s' has interval below minimum of %v",
					check.CheckID, MinInterval))
				chkType.Interval = MinInterval
			}

			http := &CheckHTTP{
				Notify:        a.state,
				CheckID:       check.CheckID,
				HTTP:          chkType.HTTP,
				Header:        chkType.Header,
				Method:        chkType.Method,
				Interval:      chkType.Interval,
				Timeout:       chkType.Timeout,
				Logger:        a.logger,
				TLSSkipVerify: chkType.TLSSkipVerify,
			}
			http.Start()
			a.checkHTTPs[check.CheckID] = http

		case chkType.IsTCP():
			if existing, ok := a.checkTCPs[check.CheckID]; ok {
				existing.Stop()
				delete(a.checkTCPs, check.CheckID)
			}
			if chkType.Interval < MinInterval {
				a.logger.Println(fmt.Sprintf("[WARN] agent: check '%s' has interval below minimum of %v",
					check.CheckID, MinInterval))
				chkType.Interval = MinInterval
			}

			tcp := &CheckTCP{
				Notify:   a.state,
				CheckID:  check.CheckID,
				TCP:      chkType.TCP,
				Interval: chkType.Interval,
				Timeout:  chkType.Timeout,
				Logger:   a.logger,
			}
			tcp.Start()
			a.checkTCPs[check.CheckID] = tcp

		case chkType.IsDocker():
			if existing, ok := a.checkDockers[check.CheckID]; ok {
				existing.Stop()
				delete(a.checkDockers, check.CheckID)
			}
			if chkType.Interval < MinInterval {
				a.logger.Println(fmt.Sprintf("[WARN] agent: check '%s' has interval below minimum of %v",
					check.CheckID, MinInterval))
				chkType.Interval = MinInterval
			}
			if chkType.Script != "" {
				a.logger.Printf("[WARN] agent: check %q has the 'script' field, which has been deprecated "+
					"and replaced with the 'args' field. See https://www.consul.io/docs/agent/checks.html",
					check.CheckID)
			}

			if a.dockerClient == nil {
				dc, err := NewDockerClient(os.Getenv("DOCKER_HOST"), CheckBufSize)
				if err != nil {
					a.logger.Printf("[ERR] agent: error creating docker client: %s", err)
					return err
				}
				a.logger.Printf("[DEBUG] agent: created docker client for %s", dc.host)
				a.dockerClient = dc
			}

			dockerCheck := &CheckDocker{
				Notify:            a.state,
				CheckID:           check.CheckID,
				DockerContainerID: chkType.DockerContainerID,
				Shell:             chkType.Shell,
				Script:            chkType.Script,
				ScriptArgs:        chkType.ScriptArgs,
				Interval:          chkType.Interval,
				Logger:            a.logger,
				client:            a.dockerClient,
			}
			dockerCheck.Start()
			a.checkDockers[check.CheckID] = dockerCheck

		case chkType.IsMonitor():
			if existing, ok := a.checkMonitors[check.CheckID]; ok {
				existing.Stop()
				delete(a.checkMonitors, check.CheckID)
			}
			if chkType.Interval < MinInterval {
				a.logger.Printf("[WARN] agent: check '%s' has interval below minimum of %v",
					check.CheckID, MinInterval)
				chkType.Interval = MinInterval
			}
			if chkType.Script != "" {
				a.logger.Printf("[WARN] agent: check %q has the 'script' field, which has been deprecated "+
					"and replaced with the 'args' field. See https://www.consul.io/docs/agent/checks.html",
					check.CheckID)
			}

			monitor := &CheckMonitor{
				Notify:     a.state,
				CheckID:    check.CheckID,
				Script:     chkType.Script,
				ScriptArgs: chkType.ScriptArgs,
				Interval:   chkType.Interval,
				Timeout:    chkType.Timeout,
				Logger:     a.logger,
			}
			monitor.Start()
			a.checkMonitors[check.CheckID] = monitor

		default:
			return fmt.Errorf("Check type is not valid")
		}

		if chkType.DeregisterCriticalServiceAfter > 0 {
			timeout := chkType.DeregisterCriticalServiceAfter
			if timeout < a.config.CheckDeregisterIntervalMin {
				timeout = a.config.CheckDeregisterIntervalMin
				a.logger.Println(fmt.Sprintf("[WARN] agent: check '%s' has deregister interval below minimum of %v",
					check.CheckID, a.config.CheckDeregisterIntervalMin))
			}
			a.checkReapAfter[check.CheckID] = timeout
		} else {
			delete(a.checkReapAfter, check.CheckID)
		}
	}

	// Add to the local state for anti-entropy
	err := a.state.AddCheck(check, token)
	if err != nil {
		a.cancelCheckMonitors(check.CheckID)
		return err
	}

	// Persist the check
	if persist && !a.config.DevMode {
		return a.persistCheck(check, chkType)
	}

	return nil
}

// RemoveCheck is used to remove a health check.
// The agent will make a best effort to ensure it is deregistered
func (a *Agent) RemoveCheck(checkID types.CheckID, persist bool) error {
	// Validate CheckID
	if checkID == "" {
		return fmt.Errorf("CheckID missing")
	}

	// Add to the local state for anti-entropy
	a.state.RemoveCheck(checkID)

	a.checkLock.Lock()
	defer a.checkLock.Unlock()

	a.cancelCheckMonitors(checkID)

	if persist {
		if err := a.purgeCheck(checkID); err != nil {
			return err
		}
		if err := a.purgeCheckState(checkID); err != nil {
			return err
		}
	}
	a.logger.Printf("[DEBUG] agent: removed check %q", checkID)
	return nil
}

func (a *Agent) cancelCheckMonitors(checkID types.CheckID) {
	// Stop any monitors
	delete(a.checkReapAfter, checkID)
	if check, ok := a.checkMonitors[checkID]; ok {
		check.Stop()
		delete(a.checkMonitors, checkID)
	}
	if check, ok := a.checkHTTPs[checkID]; ok {
		check.Stop()
		delete(a.checkHTTPs, checkID)
	}
	if check, ok := a.checkTCPs[checkID]; ok {
		check.Stop()
		delete(a.checkTCPs, checkID)
	}
	if check, ok := a.checkTTLs[checkID]; ok {
		check.Stop()
		delete(a.checkTTLs, checkID)
	}
	if check, ok := a.checkDockers[checkID]; ok {
		check.Stop()
		delete(a.checkDockers, checkID)
	}
}

// updateTTLCheck is used to update the status of a TTL check via the Agent API.
func (a *Agent) updateTTLCheck(checkID types.CheckID, status, output string) error {
	a.checkLock.Lock()
	defer a.checkLock.Unlock()

	// Grab the TTL check.
	check, ok := a.checkTTLs[checkID]
	if !ok {
		return fmt.Errorf("CheckID %q does not have associated TTL", checkID)
	}

	// Set the status through CheckTTL to reset the TTL.
	check.SetStatus(status, output)

	// We don't write any files in dev mode so bail here.
	if a.config.DevMode {
		return nil
	}

	// Persist the state so the TTL check can come up in a good state after
	// an agent restart, especially with long TTL values.
	if err := a.persistCheckState(check, status, output); err != nil {
		return fmt.Errorf("failed persisting state for check %q: %s", checkID, err)
	}

	return nil
}

// persistCheckState is used to record the check status into the data dir.
// This allows the state to be restored on a later agent start. Currently
// only useful for TTL based checks.
func (a *Agent) persistCheckState(check *CheckTTL, status, output string) error {
	// Create the persisted state
	state := persistedCheckState{
		CheckID: check.CheckID,
		Status:  status,
		Output:  output,
		Expires: time.Now().Add(check.TTL).Unix(),
	}

	// Encode the state
	buf, err := json.Marshal(state)
	if err != nil {
		return err
	}

	// Create the state dir if it doesn't exist
	dir := filepath.Join(a.config.DataDir, checkStateDir)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return fmt.Errorf("failed creating check state dir %q: %s", dir, err)
	}

	// Write the state to the file
	file := filepath.Join(dir, checkIDHash(check.CheckID))

	// Create temp file in same dir, to make more likely atomic
	tempFile := file + ".tmp"

	// persistCheckState is called frequently, so don't use writeFileAtomic to avoid calling fsync here
	if err := ioutil.WriteFile(tempFile, buf, 0600); err != nil {
		return fmt.Errorf("failed writing temp file %q: %s", tempFile, err)
	}
	if err := os.Rename(tempFile, file); err != nil {
		return fmt.Errorf("failed to rename temp file from %q to %q: %s", tempFile, file, err)
	}

	return nil
}

// loadCheckState is used to restore the persisted state of a check.
func (a *Agent) loadCheckState(check *structs.HealthCheck) error {
	// Try to read the persisted state for this check
	file := filepath.Join(a.config.DataDir, checkStateDir, checkIDHash(check.CheckID))
	buf, err := ioutil.ReadFile(file)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("failed reading file %q: %s", file, err)
	}

	// Decode the state data
	var p persistedCheckState
	if err := json.Unmarshal(buf, &p); err != nil {
		a.logger.Printf("[ERR] agent: failed decoding check state: %s", err)
		return a.purgeCheckState(check.CheckID)
	}

	// Check if the state has expired
	if time.Now().Unix() >= p.Expires {
		a.logger.Printf("[DEBUG] agent: check state expired for %q, not restoring", check.CheckID)
		return a.purgeCheckState(check.CheckID)
	}

	// Restore the fields from the state
	check.Output = p.Output
	check.Status = p.Status
	return nil
}

// purgeCheckState is used to purge the state of a check from the data dir
func (a *Agent) purgeCheckState(checkID types.CheckID) error {
	file := filepath.Join(a.config.DataDir, checkStateDir, checkIDHash(checkID))
	err := os.Remove(file)
	if os.IsNotExist(err) {
		return nil
	}
	return err
}

func (a *Agent) GossipEncrypted() bool {
	return a.delegate.Encrypted()
}

// Stats is used to get various debugging state from the sub-systems
func (a *Agent) Stats() map[string]map[string]string {
	toString := func(v uint64) string {
		return strconv.FormatUint(v, 10)
	}
	stats := a.delegate.Stats()
	stats["agent"] = map[string]string{
		"check_monitors": toString(uint64(len(a.checkMonitors))),
		"check_ttls":     toString(uint64(len(a.checkTTLs))),
		"checks":         toString(uint64(len(a.state.checks))),
		"services":       toString(uint64(len(a.state.services))),
	}

	revision := a.config.Revision
	if len(revision) > 8 {
		revision = revision[:8]
	}
	stats["build"] = map[string]string{
		"revision":   revision,
		"version":    a.config.Version,
		"prerelease": a.config.VersionPrerelease,
	}
	return stats
}

// storePid is used to write out our PID to a file if necessary
func (a *Agent) storePid() error {
	// Quit fast if no pidfile
	pidPath := a.config.PidFile
	if pidPath == "" {
		return nil
	}

	// Open the PID file
	pidFile, err := os.OpenFile(pidPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0666)
	if err != nil {
		return fmt.Errorf("Could not open pid file: %v", err)
	}
	defer pidFile.Close()

	// Write out the PID
	pid := os.Getpid()
	_, err = pidFile.WriteString(fmt.Sprintf("%d", pid))
	if err != nil {
		return fmt.Errorf("Could not write to pid file: %s", err)
	}
	return nil
}

// deletePid is used to delete our PID on exit
func (a *Agent) deletePid() error {
	// Quit fast if no pidfile
	pidPath := a.config.PidFile
	if pidPath == "" {
		return nil
	}

	stat, err := os.Stat(pidPath)
	if err != nil {
		return fmt.Errorf("Could not remove pid file: %s", err)
	}

	if stat.IsDir() {
		return fmt.Errorf("Specified pid file path is directory")
	}

	err = os.Remove(pidPath)
	if err != nil {
		return fmt.Errorf("Could not remove pid file: %s", err)
	}
	return nil
}

// loadServices will load service definitions from configuration and persisted
// definitions on disk, and load them into the local agent.
func (a *Agent) loadServices(conf *config.RuntimeConfig) error {
	// Register the services from config
	for _, service := range conf.Services {
		ns := service.NodeService()
		chkTypes, err := service.CheckTypes()
		if err != nil {
			return fmt.Errorf("Failed to validate checks for service %q: %v", service.Name, err)
		}
		if err := a.AddService(ns, chkTypes, false, service.Token); err != nil {
			return fmt.Errorf("Failed to register service %q: %v", service.Name, err)
		}
	}

	// Load any persisted services
	svcDir := filepath.Join(a.config.DataDir, servicesDir)
	files, err := ioutil.ReadDir(svcDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("Failed reading services dir %q: %s", svcDir, err)
	}
	for _, fi := range files {
		// Skip all dirs
		if fi.IsDir() {
			continue
		}

		// Skip all partially written temporary files
		if strings.HasSuffix(fi.Name(), "tmp") {
			a.logger.Printf("[WARN] Ignoring temporary service file %v", fi.Name())
			continue
		}

		// Open the file for reading
		file := filepath.Join(svcDir, fi.Name())
		fh, err := os.Open(file)
		if err != nil {
			return fmt.Errorf("failed opening service file %q: %s", file, err)
		}

		// Read the contents into a buffer
		buf, err := ioutil.ReadAll(fh)
		fh.Close()
		if err != nil {
			return fmt.Errorf("failed reading service file %q: %s", file, err)
		}

		// Try decoding the service definition
		var p persistedService
		if err := json.Unmarshal(buf, &p); err != nil {
			// Backwards-compatibility for pre-0.5.1 persisted services
			if err := json.Unmarshal(buf, &p.Service); err != nil {
				return fmt.Errorf("failed decoding service file %q: %s", file, err)
			}
		}
		serviceID := p.Service.ID

		if _, ok := a.state.services[serviceID]; ok {
			// Purge previously persisted service. This allows config to be
			// preferred over services persisted from the API.
			a.logger.Printf("[DEBUG] agent: service %q exists, not restoring from %q",
				serviceID, file)
			if err := a.purgeService(serviceID); err != nil {
				return fmt.Errorf("failed purging service %q: %s", serviceID, err)
			}
		} else {
			a.logger.Printf("[DEBUG] agent: restored service definition %q from %q",
				serviceID, file)
			if err := a.AddService(p.Service, nil, false, p.Token); err != nil {
				return fmt.Errorf("failed adding service %q: %s", serviceID, err)
			}
		}
	}

	return nil
}

// unloadServices will deregister all services other than the 'consul' service
// known to the local agent.
func (a *Agent) unloadServices() error {
	for _, service := range a.state.Services() {
		if err := a.RemoveService(service.ID, false); err != nil {
			return fmt.Errorf("Failed deregistering service '%s': %v", service.ID, err)
		}
	}

	return nil
}

// loadChecks loads check definitions and/or persisted check definitions from
// disk and re-registers them with the local agent.
func (a *Agent) loadChecks(conf *config.RuntimeConfig) error {
	// Register the checks from config
	for _, check := range conf.Checks {
		health := check.HealthCheck(conf.NodeName)
		chkType := check.CheckType()
		if err := a.AddCheck(health, chkType, false, check.Token); err != nil {
			return fmt.Errorf("Failed to register check '%s': %v %v", check.Name, err, check)
		}
	}

	// Load any persisted checks
	checkDir := filepath.Join(a.config.DataDir, checksDir)
	files, err := ioutil.ReadDir(checkDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("Failed reading checks dir %q: %s", checkDir, err)
	}
	for _, fi := range files {
		// Ignore dirs - we only care about the check definition files
		if fi.IsDir() {
			continue
		}

		// Open the file for reading
		file := filepath.Join(checkDir, fi.Name())
		fh, err := os.Open(file)
		if err != nil {
			return fmt.Errorf("Failed opening check file %q: %s", file, err)
		}

		// Read the contents into a buffer
		buf, err := ioutil.ReadAll(fh)
		fh.Close()
		if err != nil {
			return fmt.Errorf("failed reading check file %q: %s", file, err)
		}

		// Decode the check
		var p persistedCheck
		if err := json.Unmarshal(buf, &p); err != nil {
			return fmt.Errorf("Failed decoding check file %q: %s", file, err)
		}
		checkID := p.Check.CheckID

		if _, ok := a.state.checks[checkID]; ok {
			// Purge previously persisted check. This allows config to be
			// preferred over persisted checks from the API.
			a.logger.Printf("[DEBUG] agent: check %q exists, not restoring from %q",
				checkID, file)
			if err := a.purgeCheck(checkID); err != nil {
				return fmt.Errorf("Failed purging check %q: %s", checkID, err)
			}
		} else {
			// Default check to critical to avoid placing potentially unhealthy
			// services into the active pool
			p.Check.Status = api.HealthCritical

			if err := a.AddCheck(p.Check, p.ChkType, false, p.Token); err != nil {
				// Purge the check if it is unable to be restored.
				a.logger.Printf("[WARN] agent: Failed to restore check %q: %s",
					checkID, err)
				if err := a.purgeCheck(checkID); err != nil {
					return fmt.Errorf("Failed purging check %q: %s", checkID, err)
				}
			}
			a.logger.Printf("[DEBUG] agent: restored health check %q from %q",
				p.Check.CheckID, file)
		}
	}

	return nil
}

// unloadChecks will deregister all checks known to the local agent.
func (a *Agent) unloadChecks() error {
	for _, check := range a.state.Checks() {
		if err := a.RemoveCheck(check.CheckID, false); err != nil {
			return fmt.Errorf("Failed deregistering check '%s': %s", check.CheckID, err)
		}
	}

	return nil
}

// snapshotCheckState is used to snapshot the current state of the health
// checks. This is done before we reload our checks, so that we can properly
// restore into the same state.
func (a *Agent) snapshotCheckState() map[types.CheckID]*structs.HealthCheck {
	return a.state.Checks()
}

// restoreCheckState is used to reset the health state based on a snapshot.
// This is done after we finish the reload to avoid any unnecessary flaps
// in health state and potential session invalidations.
func (a *Agent) restoreCheckState(snap map[types.CheckID]*structs.HealthCheck) {
	for id, check := range snap {
		a.state.UpdateCheck(id, check.Status, check.Output)
	}
}

// loadMetadata loads node metadata fields from the agent config and
// updates them on the local agent.
func (a *Agent) loadMetadata(conf *config.RuntimeConfig) error {
	a.state.Lock()
	defer a.state.Unlock()

	for key, value := range conf.NodeMeta {
		a.state.metadata[key] = value
	}

	a.state.metadata[structs.MetaSegmentKey] = conf.SegmentName

	a.state.changeMade()

	return nil
}

// unloadMetadata resets the local metadata state
func (a *Agent) unloadMetadata() {
	a.state.Lock()
	defer a.state.Unlock()

	a.state.metadata = make(map[string]string)
}

// serviceMaintCheckID returns the ID of a given service's maintenance check
func serviceMaintCheckID(serviceID string) types.CheckID {
	return types.CheckID(structs.ServiceMaintPrefix + serviceID)
}

// EnableServiceMaintenance will register a false health check against the given
// service ID with critical status. This will exclude the service from queries.
func (a *Agent) EnableServiceMaintenance(serviceID, reason, token string) error {
	service, ok := a.state.Services()[serviceID]
	if !ok {
		return fmt.Errorf("No service registered with ID %q", serviceID)
	}

	// Check if maintenance mode is not already enabled
	checkID := serviceMaintCheckID(serviceID)
	if _, ok := a.state.Checks()[checkID]; ok {
		return nil
	}

	// Use default notes if no reason provided
	if reason == "" {
		reason = defaultServiceMaintReason
	}

	// Create and register the critical health check
	check := &structs.HealthCheck{
		Node:        a.config.NodeName,
		CheckID:     checkID,
		Name:        "Service Maintenance Mode",
		Notes:       reason,
		ServiceID:   service.ID,
		ServiceName: service.Service,
		Status:      api.HealthCritical,
	}
	a.AddCheck(check, nil, true, token)
	a.logger.Printf("[INFO] agent: Service %q entered maintenance mode", serviceID)

	return nil
}

// DisableServiceMaintenance will deregister the fake maintenance mode check
// if the service has been marked as in maintenance.
func (a *Agent) DisableServiceMaintenance(serviceID string) error {
	if _, ok := a.state.Services()[serviceID]; !ok {
		return fmt.Errorf("No service registered with ID %q", serviceID)
	}

	// Check if maintenance mode is enabled
	checkID := serviceMaintCheckID(serviceID)
	if _, ok := a.state.Checks()[checkID]; !ok {
		return nil
	}

	// Deregister the maintenance check
	a.RemoveCheck(checkID, true)
	a.logger.Printf("[INFO] agent: Service %q left maintenance mode", serviceID)

	return nil
}

// EnableNodeMaintenance places a node into maintenance mode.
func (a *Agent) EnableNodeMaintenance(reason, token string) {
	// Ensure node maintenance is not already enabled
	if _, ok := a.state.Checks()[structs.NodeMaint]; ok {
		return
	}

	// Use a default notes value
	if reason == "" {
		reason = defaultNodeMaintReason
	}

	// Create and register the node maintenance check
	check := &structs.HealthCheck{
		Node:    a.config.NodeName,
		CheckID: structs.NodeMaint,
		Name:    "Node Maintenance Mode",
		Notes:   reason,
		Status:  api.HealthCritical,
	}
	a.AddCheck(check, nil, true, token)
	a.logger.Printf("[INFO] agent: Node entered maintenance mode")
}

// DisableNodeMaintenance removes a node from maintenance mode
func (a *Agent) DisableNodeMaintenance() {
	if _, ok := a.state.Checks()[structs.NodeMaint]; !ok {
		return
	}
	a.RemoveCheck(structs.NodeMaint, true)
	a.logger.Printf("[INFO] agent: Node left maintenance mode")
}

func (a *Agent) ReloadConfig(newCfg *config.RuntimeConfig) error {
	// Bulk update the services and checks
	a.PauseSync()
	defer a.ResumeSync()

	// Snapshot the current state, and restore it afterwards
	snap := a.snapshotCheckState()
	defer a.restoreCheckState(snap)

	// First unload all checks, services, and metadata. This lets us begin the reload
	// with a clean slate.
	if err := a.unloadServices(); err != nil {
		return fmt.Errorf("Failed unloading services: %s", err)
	}
	if err := a.unloadChecks(); err != nil {
		return fmt.Errorf("Failed unloading checks: %s", err)
	}
	a.unloadMetadata()

	// Reload service/check definitions and metadata.
	if err := a.loadServices(newCfg); err != nil {
		return fmt.Errorf("Failed reloading services: %s", err)
	}
	if err := a.loadChecks(newCfg); err != nil {
		return fmt.Errorf("Failed reloading checks: %s", err)
	}
	if err := a.loadMetadata(newCfg); err != nil {
		return fmt.Errorf("Failed reloading metadata: %s", err)
	}

	if err := a.reloadWatches(newCfg); err != nil {
		return fmt.Errorf("Failed reloading watches: %v", err)
	}

	// Update filtered metrics
	metrics.UpdateFilter(newCfg.TelemetryAllowedPrefixes, newCfg.TelemetryBlockedPrefixes)

	a.state.SetDiscardCheckOutput(newCfg.DiscardCheckOutput)

	return nil
}
