// Copyright (c) HashiCorp, Inc.
// SPDX-License-Identifier: MPL-2.0

package api

import (
	"bufio"
	"fmt"
)

// AgentCheck represents a check known to the agent
type AgentCheck struct {
	Node        string
	CheckID     string
	Name        string
	Status      string
	Notes       string
	Output      string
	ServiceID   string
	ServiceName string
}

// AgentService represents a service known to the agent
type AgentService struct {
	ID                string
	Service           string
	Tags              []string
	Port              int
	Address           string
	EnableTagOverride bool
	CreateIndex       uint64
	ModifyIndex       uint64
}

// AgentMember represents a cluster member known to the agent
type AgentMember struct {
	Name        string
	Addr        string
	Port        uint16
	Tags        map[string]string
	Status      int
	ProtocolMin uint8
	ProtocolMax uint8
	ProtocolCur uint8
	DelegateMin uint8
	DelegateMax uint8
	DelegateCur uint8
}

// AllSegments is used to select for all segments in MembersOpts.
const AllSegments = "_all"

// MembersOpts is used for querying member information.
type MembersOpts struct {
	// WAN is whether to show members from the WAN.
	WAN bool

	// Segment is the LAN segment to show members for. Setting this to the
	// AllSegments value above will show members in all segments.
	Segment string
}

// AgentServiceRegistration is used to register a new service
type AgentServiceRegistration struct {
	ID                string   `json:",omitempty"`
	Name              string   `json:",omitempty"`
	Tags              []string `json:",omitempty"`
	Port              int      `json:",omitempty"`
	Address           string   `json:",omitempty"`
	EnableTagOverride bool     `json:",omitempty"`
	Check             *AgentServiceCheck
	Checks            AgentServiceChecks
}

// AgentCheckRegistration is used to register a new check
type AgentCheckRegistration struct {
	ID        string `json:",omitempty"`
	Name      string `json:",omitempty"`
	Notes     string `json:",omitempty"`
	ServiceID string `json:",omitempty"`
	AgentServiceCheck
}

// AgentServiceCheck is used to define a node or service level check
type AgentServiceCheck struct {
	Script            string              `json:",omitempty"`
	DockerContainerID string              `json:",omitempty"`
	Shell             string              `json:",omitempty"` // Only supported for Docker.
	Interval          string              `json:",omitempty"`
	Timeout           string              `json:",omitempty"`
	TTL               string              `json:",omitempty"`
	HTTP              string              `json:",omitempty"`
	Header            map[string][]string `json:",omitempty"`
	Method            string              `json:",omitempty"`
	TCP               string              `json:",omitempty"`
	Status            string              `json:",omitempty"`
	Notes             string              `json:",omitempty"`
	TLSSkipVerify     bool                `json:",omitempty"`

	// In Consul 0.7 and later, checks that are associated with a service
	// may also contain this optional DeregisterCriticalServiceAfter field,
	// which is a timeout in the same Go time format as Interval and TTL. If
	// a check is in the critical state for more than this configured value,
	// then its associated service (and all of its associated checks) will
	// automatically be deregistered.
	DeregisterCriticalServiceAfter string `json:",omitempty"`
}
type AgentServiceChecks []*AgentServiceCheck

// AgentToken is used when updating ACL tokens for an agent.
type AgentToken struct {
	Token string
}

// Metrics info is used to store different types of metric values from the agent.
type MetricsInfo struct {
	Timestamp string
	Gauges    []GaugeValue
	Points    []PointValue
	Counters  []SampledValue
	Samples   []SampledValue
}

// GaugeValue stores one value that is updated as time goes on, such as
// the amount of memory allocated.
type GaugeValue struct {
	Name   string
	Value  float32
	Labels map[string]string
}

// PointValue holds a series of points for a metric.
type PointValue struct {
	Name   string
	Points []float32
}

// SampledValue stores info about a metric that is incremented over time,
// such as the number of requests to an HTTP endpoint.
type SampledValue struct {
	Name   string
	Count  int
	Sum    float64
	Min    float64
	Max    float64
	Mean   float64
	Stddev float64
	Labels map[string]string
}

// Agent can be used to query the Agent endpoints
type Agent struct {
	c *Client

	// cache the node name
	nodeName string
}

// Agent returns a handle to the agent endpoints
func (c *Client) Agent() *Agent {
	return &Agent{c: c}
}

// Self is used to query the agent we are speaking to for
// information about itself
func (a *Agent) Self() (map[string]map[string]interface{}, error) {
	r := a.c.newRequest("GET", "/v1/agent/self")
	_, resp, err := requireOK(a.c.doRequest(r))
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	var out map[string]map[string]interface{}
	if err := decodeBody(resp, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// Metrics is used to query the agent we are speaking to for
// its current internal metric data
func (a *Agent) Metrics() (*MetricsInfo, error) {
	r := a.c.newRequest("GET", "/v1/agent/metrics")
	_, resp, err := requireOK(a.c.doRequest(r))
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	var out *MetricsInfo
	if err := decodeBody(resp, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// Reload triggers a configuration reload for the agent we are connected to.
func (a *Agent) Reload() error {
	r := a.c.newRequest("PUT", "/v1/agent/reload")
	_, resp, err := requireOK(a.c.doRequest(r))
	if err != nil {
		return err
	}
	resp.Body.Close()
	return nil
}

// NodeName is used to get the node name of the agent
func (a *Agent) NodeName() (string, error) {
	if a.nodeName != "" {
		return a.nodeName, nil
	}
	info, err := a.Self()
	if err != nil {
		return "", err
	}
	name := info["Config"]["NodeName"].(string)
	a.nodeName = name
	return name, nil
}

// Checks returns the locally registered checks
func (a *Agent) Checks() (map[string]*AgentCheck, error) {
	r := a.c.newRequest("GET", "/v1/agent/checks")
	_, resp, err := requireOK(a.c.doRequest(r))
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	var out map[string]*AgentCheck
	if err := decodeBody(resp, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// Services returns the locally registered services
func (a *Agent) Services() (map[string]*AgentService, error) {
	r := a.c.newRequest("GET", "/v1/agent/services")
	_, resp, err := requireOK(a.c.doRequest(r))
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	var out map[string]*AgentService
	if err := decodeBody(resp, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// Members returns the known gossip members. The WAN
// flag can be used to query a server for WAN members.
func (a *Agent) Members(wan bool) ([]*AgentMember, error) {
	r := a.c.newRequest("GET", "/v1/agent/members")
	if wan {
		r.params.Set("wan", "1")
	}
	_, resp, err := requireOK(a.c.doRequest(r))
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	var out []*AgentMember
	if err := decodeBody(resp, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// MembersOpts returns the known gossip members and can be passed
// additional options for WAN/segment filtering.
func (a *Agent) MembersOpts(opts MembersOpts) ([]*AgentMember, error) {
	r := a.c.newRequest("GET", "/v1/agent/members")
	r.params.Set("segment", opts.Segment)
	if opts.WAN {
		r.params.Set("wan", "1")
	}

	_, resp, err := requireOK(a.c.doRequest(r))
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	var out []*AgentMember
	if err := decodeBody(resp, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// ServiceRegister is used to register a new service with
// the local agent
func (a *Agent) ServiceRegister(service *AgentServiceRegistration) error {
	r := a.c.newRequest("PUT", "/v1/agent/service/register")
	r.obj = service
	_, resp, err := requireOK(a.c.doRequest(r))
	if err != nil {
		return err
	}
	resp.Body.Close()
	return nil
}

// ServiceDeregister is used to deregister a service with
// the local agent
func (a *Agent) ServiceDeregister(serviceID string) error {
	r := a.c.newRequest("PUT", "/v1/agent/service/deregister/"+serviceID)
	_, resp, err := requireOK(a.c.doRequest(r))
	if err != nil {
		return err
	}
	resp.Body.Close()
	return nil
}

// PassTTL is used to set a TTL check to the passing state.
//
// DEPRECATION NOTICE: This interface is deprecated in favor of UpdateTTL().
// The client interface will be removed in 0.8 or changed to use
// UpdateTTL()'s endpoint and the server endpoints will be removed in 0.9.
func (a *Agent) PassTTL(checkID, note string) error {
	return a.updateTTL(checkID, note, "pass")
}

// WarnTTL is used to set a TTL check to the warning state.
//
// DEPRECATION NOTICE: This interface is deprecated in favor of UpdateTTL().
// The client interface will be removed in 0.8 or changed to use
// UpdateTTL()'s endpoint and the server endpoints will be removed in 0.9.
func (a *Agent) WarnTTL(checkID, note string) error {
	return a.updateTTL(checkID, note, "warn")
}

// FailTTL is used to set a TTL check to the failing state.
//
// DEPRECATION NOTICE: This interface is deprecated in favor of UpdateTTL().
// The client interface will be removed in 0.8 or changed to use
// UpdateTTL()'s endpoint and the server endpoints will be removed in 0.9.
func (a *Agent) FailTTL(checkID, note string) error {
	return a.updateTTL(checkID, note, "fail")
}

// updateTTL is used to update the TTL of a check. This is the internal
// method that uses the old API that's present in Consul versions prior to
// 0.6.4. Since Consul didn't have an analogous "update" API before it seemed
// ok to break this (former) UpdateTTL in favor of the new UpdateTTL below,
// but keep the old Pass/Warn/Fail methods using the old API under the hood.
//
// DEPRECATION NOTICE: This interface is deprecated in favor of UpdateTTL().
// The client interface will be removed in 0.8 and the server endpoints will
// be removed in 0.9.
func (a *Agent) updateTTL(checkID, note, status string) error {
	switch status {
	case "pass":
	case "warn":
	case "fail":
	default:
		return fmt.Errorf("Invalid status: %s", status)
	}
	endpoint := fmt.Sprintf("/v1/agent/check/%s/%s", status, checkID)
	r := a.c.newRequest("PUT", endpoint)
	r.params.Set("note", note)
	_, resp, err := requireOK(a.c.doRequest(r))
	if err != nil {
		return err
	}
	resp.Body.Close()
	return nil
}

// checkUpdate is the payload for a PUT for a check update.
type checkUpdate struct {
	// Status is one of the api.Health* states: HealthPassing
	// ("passing"), HealthWarning ("warning"), or HealthCritical
	// ("critical").
	Status string

	// Output is the information to post to the UI for operators as the
	// output of the process that decided to hit the TTL check. This is
	// different from the note field that's associated with the check
	// itself.
	Output string
}

// UpdateTTL is used to update the TTL of a check. This uses the newer API
// that was introduced in Consul 0.6.4 and later. We translate the old status
// strings for compatibility (though a newer version of Consul will still be
// required to use this API).
func (a *Agent) UpdateTTL(checkID, output, status string) error {
	switch status {
	case "pass", HealthPassing:
		status = HealthPassing
	case "warn", HealthWarning:
		status = HealthWarning
	case "fail", HealthCritical:
		status = HealthCritical
	default:
		return fmt.Errorf("Invalid status: %s", status)
	}

	endpoint := fmt.Sprintf("/v1/agent/check/update/%s", checkID)
	r := a.c.newRequest("PUT", endpoint)
	r.obj = &checkUpdate{
		Status: status,
		Output: output,
	}

	_, resp, err := requireOK(a.c.doRequest(r))
	if err != nil {
		return err
	}
	resp.Body.Close()
	return nil
}

// CheckRegister is used to register a new check with
// the local agent
func (a *Agent) CheckRegister(check *AgentCheckRegistration) error {
	r := a.c.newRequest("PUT", "/v1/agent/check/register")
	r.obj = check
	_, resp, err := requireOK(a.c.doRequest(r))
	if err != nil {
		return err
	}
	resp.Body.Close()
	return nil
}

// CheckDeregister is used to deregister a check with
// the local agent
func (a *Agent) CheckDeregister(checkID string) error {
	r := a.c.newRequest("PUT", "/v1/agent/check/deregister/"+checkID)
	_, resp, err := requireOK(a.c.doRequest(r))
	if err != nil {
		return err
	}
	resp.Body.Close()
	return nil
}

// Join is used to instruct the agent to attempt a join to
// another cluster member
func (a *Agent) Join(addr string, wan bool) error {
	r := a.c.newRequest("PUT", "/v1/agent/join/"+addr)
	if wan {
		r.params.Set("wan", "1")
	}
	_, resp, err := requireOK(a.c.doRequest(r))
	if err != nil {
		return err
	}
	resp.Body.Close()
	return nil
}

// Leave is used to have the agent gracefully leave the cluster and shutdown
func (a *Agent) Leave() error {
	r := a.c.newRequest("PUT", "/v1/agent/leave")
	_, resp, err := requireOK(a.c.doRequest(r))
	if err != nil {
		return err
	}
	resp.Body.Close()
	return nil
}

// ForceLeave is used to have the agent eject a failed node
func (a *Agent) ForceLeave(node string) error {
	r := a.c.newRequest("PUT", "/v1/agent/force-leave/"+node)
	_, resp, err := requireOK(a.c.doRequest(r))
	if err != nil {
		return err
	}
	resp.Body.Close()
	return nil
}

// EnableServiceMaintenance toggles service maintenance mode on
// for the given service ID.
func (a *Agent) EnableServiceMaintenance(serviceID, reason string) error {
	r := a.c.newRequest("PUT", "/v1/agent/service/maintenance/"+serviceID)
	r.params.Set("enable", "true")
	r.params.Set("reason", reason)
	_, resp, err := requireOK(a.c.doRequest(r))
	if err != nil {
		return err
	}
	resp.Body.Close()
	return nil
}

// DisableServiceMaintenance toggles service maintenance mode off
// for the given service ID.
func (a *Agent) DisableServiceMaintenance(serviceID string) error {
	r := a.c.newRequest("PUT", "/v1/agent/service/maintenance/"+serviceID)
	r.params.Set("enable", "false")
	_, resp, err := requireOK(a.c.doRequest(r))
	if err != nil {
		return err
	}
	resp.Body.Close()
	return nil
}

// EnableNodeMaintenance toggles node maintenance mode on for the
// agent we are connected to.
func (a *Agent) EnableNodeMaintenance(reason string) error {
	r := a.c.newRequest("PUT", "/v1/agent/maintenance")
	r.params.Set("enable", "true")
	r.params.Set("reason", reason)
	_, resp, err := requireOK(a.c.doRequest(r))
	if err != nil {
		return err
	}
	resp.Body.Close()
	return nil
}

// DisableNodeMaintenance toggles node maintenance mode off for the
// agent we are connected to.
func (a *Agent) DisableNodeMaintenance() error {
	r := a.c.newRequest("PUT", "/v1/agent/maintenance")
	r.params.Set("enable", "false")
	_, resp, err := requireOK(a.c.doRequest(r))
	if err != nil {
		return err
	}
	resp.Body.Close()
	return nil
}

// Monitor returns a channel which will receive streaming logs from the agent
// Providing a non-nil stopCh can be used to close the connection and stop the
// log stream. An empty string will be sent down the given channel when there's
// nothing left to stream, after which the caller should close the stopCh.
func (a *Agent) Monitor(loglevel string, stopCh <-chan struct{}, q *QueryOptions) (chan string, error) {
	r := a.c.newRequest("GET", "/v1/agent/monitor")
	r.setQueryOptions(q)
	if loglevel != "" {
		r.params.Add("loglevel", loglevel)
	}
	_, resp, err := requireOK(a.c.doRequest(r))
	if err != nil {
		return nil, err
	}

	logCh := make(chan string, 64)
	go func() {
		defer resp.Body.Close()

		scanner := bufio.NewScanner(resp.Body)
		for {
			select {
			case <-stopCh:
				close(logCh)
				return
			default:
			}
			if scanner.Scan() {
				// An empty string signals to the caller that
				// the scan is done, so make sure we only emit
				// that when the scanner says it's done, not if
				// we happen to ingest an empty line.
				if text := scanner.Text(); text != "" {
					logCh <- text
				} else {
					logCh <- " "
				}
			} else {
				logCh <- ""
			}
		}
	}()

	return logCh, nil
}

// UpdateACLToken updates the agent's "acl_token". See updateToken for more
// details.
func (c *Agent) UpdateACLToken(token string, q *WriteOptions) (*WriteMeta, error) {
	return c.updateToken("acl_token", token, q)
}

// UpdateACLAgentToken updates the agent's "acl_agent_token". See updateToken
// for more details.
func (c *Agent) UpdateACLAgentToken(token string, q *WriteOptions) (*WriteMeta, error) {
	return c.updateToken("acl_agent_token", token, q)
}

// UpdateACLAgentMasterToken updates the agent's "acl_agent_master_token". See
// updateToken for more details.
func (c *Agent) UpdateACLAgentMasterToken(token string, q *WriteOptions) (*WriteMeta, error) {
	return c.updateToken("acl_agent_master_token", token, q)
}

// UpdateACLReplicationToken updates the agent's "acl_replication_token". See
// updateToken for more details.
func (c *Agent) UpdateACLReplicationToken(token string, q *WriteOptions) (*WriteMeta, error) {
	return c.updateToken("acl_replication_token", token, q)
}

// updateToken can be used to update an agent's ACL token after the agent has
// started. The tokens are not persisted, so will need to be updated again if
// the agent is restarted.
func (c *Agent) updateToken(target, token string, q *WriteOptions) (*WriteMeta, error) {
	r := c.c.newRequest("PUT", fmt.Sprintf("/v1/agent/token/%s", target))
	r.setWriteOptions(q)
	r.obj = &AgentToken{Token: token}
	rtt, resp, err := requireOK(c.c.doRequest(r))
	if err != nil {
		return nil, err
	}
	resp.Body.Close()

	wm := &WriteMeta{RequestTime: rtt}
	return wm, nil
}
