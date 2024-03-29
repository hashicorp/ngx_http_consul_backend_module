// Copyright (c) HashiCorp, Inc.
// SPDX-License-Identifier: MPL-2.0

package consul

import (
	"fmt"
	"time"

	"github.com/armon/go-metrics"
	"github.com/hashicorp/consul/acl"
	"github.com/hashicorp/consul/agent/consul/state"
	"github.com/hashicorp/consul/agent/structs"
	"github.com/hashicorp/go-memdb"
	"github.com/hashicorp/go-uuid"
)

// ACL endpoint is used to manipulate ACLs
type ACL struct {
	srv *Server
}

// Bootstrap is used to perform a one-time ACL bootstrap operation on
// a cluster to get the first management token.
func (a *ACL) Bootstrap(args *structs.DCSpecificRequest, reply *structs.ACL) error {
	if done, err := a.srv.forward("ACL.Bootstrap", args, args, reply); done {
		return err
	}

	// Verify we are allowed to serve this request
	if a.srv.config.ACLDatacenter != a.srv.config.Datacenter {
		return acl.ErrDisabled
	}

	// By doing some pre-checks we can head off later bootstrap attempts
	// without having to run them through Raft, which should curb abuse.
	state := a.srv.fsm.State()
	bs, err := state.ACLGetBootstrap()
	if err != nil {
		return err
	}
	if bs == nil {
		return structs.ACLBootstrapNotInitializedErr
	}
	if !bs.AllowBootstrap {
		return structs.ACLBootstrapNotAllowedErr
	}

	// Propose a new token.
	token, err := uuid.GenerateUUID()
	if err != nil {
		return fmt.Errorf("failed to make random token: %v", err)
	}

	// Attempt a bootstrap.
	req := structs.ACLRequest{
		Datacenter: a.srv.config.ACLDatacenter,
		Op:         structs.ACLBootstrapNow,
		ACL: structs.ACL{
			ID:   token,
			Name: "Bootstrap Token",
			Type: structs.ACLTypeManagement,
		},
	}
	resp, err := a.srv.raftApply(structs.ACLRequestType, &req)
	if err != nil {
		return err
	}
	switch v := resp.(type) {
	case error:
		return v

	case *structs.ACL:
		*reply = *v

	default:
		// Just log this, since it looks like the bootstrap may have
		// completed.
		a.srv.logger.Printf("[ERR] consul.acl: Unexpected response during bootstrap: %T", v)
	}

	a.srv.logger.Printf("[INFO] consul.acl: ACL bootstrap completed")
	return nil
}

// aclApplyInternal is used to apply an ACL request after it has been vetted that
// this is a valid operation. It is used when users are updating ACLs, in which
// case we check their token to make sure they have management privileges. It is
// also used for ACL replication. We want to run the replicated ACLs through the
// same checks on the change itself.
func aclApplyInternal(srv *Server, args *structs.ACLRequest, reply *string) error {
	// All ACLs must have an ID by this point.
	if args.ACL.ID == "" {
		return fmt.Errorf("Missing ACL ID")
	}

	switch args.Op {
	case structs.ACLSet:
		// Verify the ACL type
		switch args.ACL.Type {
		case structs.ACLTypeClient:
		case structs.ACLTypeManagement:
		default:
			return fmt.Errorf("Invalid ACL Type")
		}

		// Verify this is not a root ACL
		if acl.RootACL(args.ACL.ID) != nil {
			return acl.PermissionDeniedError{Cause: "Cannot modify root ACL"}
		}

		// Validate the rules compile
		_, err := acl.Parse(args.ACL.Rules, srv.sentinel)
		if err != nil {
			return fmt.Errorf("ACL rule compilation failed: %v", err)
		}

	case structs.ACLDelete:
		if args.ACL.ID == anonymousToken {
			return acl.PermissionDeniedError{Cause: "Cannot delete anonymous token"}
		}

	default:
		return fmt.Errorf("Invalid ACL Operation")
	}

	// Apply the update
	resp, err := srv.raftApply(structs.ACLRequestType, args)
	if err != nil {
		srv.logger.Printf("[ERR] consul.acl: Apply failed: %v", err)
		return err
	}
	if respErr, ok := resp.(error); ok {
		return respErr
	}

	// Check if the return type is a string
	if respString, ok := resp.(string); ok {
		*reply = respString
	}

	return nil
}

// Apply is used to apply a modifying request to the data store. This should
// only be used for operations that modify the data
func (a *ACL) Apply(args *structs.ACLRequest, reply *string) error {
	if done, err := a.srv.forward("ACL.Apply", args, args, reply); done {
		return err
	}
	defer metrics.MeasureSince([]string{"consul", "acl", "apply"}, time.Now())
	defer metrics.MeasureSince([]string{"acl", "apply"}, time.Now())

	// Verify we are allowed to serve this request
	if a.srv.config.ACLDatacenter != a.srv.config.Datacenter {
		return acl.ErrDisabled
	}

	// Verify token is permitted to modify ACLs
	if rule, err := a.srv.resolveToken(args.Token); err != nil {
		return err
	} else if rule == nil || !rule.ACLModify() {
		return acl.ErrPermissionDenied
	}

	// If no ID is provided, generate a new ID. This must be done prior to
	// appending to the Raft log, because the ID is not deterministic. Once
	// the entry is in the log, the state update MUST be deterministic or
	// the followers will not converge.
	if args.Op == structs.ACLSet && args.ACL.ID == "" {
		state := a.srv.fsm.State()
		for {
			var err error
			args.ACL.ID, err = uuid.GenerateUUID()
			if err != nil {
				a.srv.logger.Printf("[ERR] consul.acl: UUID generation failed: %v", err)
				return err
			}

			_, acl, err := state.ACLGet(nil, args.ACL.ID)
			if err != nil {
				a.srv.logger.Printf("[ERR] consul.acl: ACL lookup failed: %v", err)
				return err
			}
			if acl == nil {
				break
			}
		}
	}

	// Do the apply now that this update is vetted.
	if err := aclApplyInternal(a.srv, args, reply); err != nil {
		return err
	}

	// Clear the cache if applicable
	if args.ACL.ID != "" {
		a.srv.aclAuthCache.ClearACL(args.ACL.ID)
	}

	return nil
}

// Get is used to retrieve a single ACL
func (a *ACL) Get(args *structs.ACLSpecificRequest,
	reply *structs.IndexedACLs) error {
	if done, err := a.srv.forward("ACL.Get", args, args, reply); done {
		return err
	}

	// Verify we are allowed to serve this request
	if a.srv.config.ACLDatacenter != a.srv.config.Datacenter {
		return acl.ErrDisabled
	}

	return a.srv.blockingQuery(&args.QueryOptions,
		&reply.QueryMeta,
		func(ws memdb.WatchSet, state *state.Store) error {
			index, acl, err := state.ACLGet(ws, args.ACL)
			if err != nil {
				return err
			}

			reply.Index = index
			if acl != nil {
				reply.ACLs = structs.ACLs{acl}
			} else {
				reply.ACLs = nil
			}
			return nil
		})
}

// makeACLETag returns an ETag for the given parent and policy.
func makeACLETag(parent string, policy *acl.Policy) string {
	return fmt.Sprintf("%s:%s", parent, policy.ID)
}

// GetPolicy is used to retrieve a compiled policy object with a TTL. Does not
// support a blocking query.
func (a *ACL) GetPolicy(args *structs.ACLPolicyRequest, reply *structs.ACLPolicy) error {
	if done, err := a.srv.forward("ACL.GetPolicy", args, args, reply); done {
		return err
	}

	// Verify we are allowed to serve this request
	if a.srv.config.ACLDatacenter != a.srv.config.Datacenter {
		return acl.ErrDisabled
	}

	// Get the policy via the cache
	parent, policy, err := a.srv.aclAuthCache.GetACLPolicy(args.ACL)
	if err != nil {
		return err
	}

	// Generate an ETag
	conf := a.srv.config
	etag := makeACLETag(parent, policy)

	// Setup the response
	reply.ETag = etag
	reply.TTL = conf.ACLTTL
	a.srv.setQueryMeta(&reply.QueryMeta)

	// Only send the policy on an Etag mis-match
	if args.ETag != etag {
		reply.Parent = parent
		reply.Policy = policy
	}
	return nil
}

// List is used to list all the ACLs
func (a *ACL) List(args *structs.DCSpecificRequest,
	reply *structs.IndexedACLs) error {
	if done, err := a.srv.forward("ACL.List", args, args, reply); done {
		return err
	}

	// Verify we are allowed to serve this request
	if a.srv.config.ACLDatacenter != a.srv.config.Datacenter {
		return acl.ErrDisabled
	}

	// Verify token is permitted to list ACLs
	if rule, err := a.srv.resolveToken(args.Token); err != nil {
		return err
	} else if rule == nil || !rule.ACLList() {
		return acl.ErrPermissionDenied
	}

	return a.srv.blockingQuery(&args.QueryOptions,
		&reply.QueryMeta,
		func(ws memdb.WatchSet, state *state.Store) error {
			index, acls, err := state.ACLList(ws)
			if err != nil {
				return err
			}

			reply.Index, reply.ACLs = index, acls
			return nil
		})
}

// ReplicationStatus is used to retrieve the current ACL replication status.
func (a *ACL) ReplicationStatus(args *structs.DCSpecificRequest,
	reply *structs.ACLReplicationStatus) error {
	// This must be sent to the leader, so we fix the args since we are
	// re-using a structure where we don't support all the options.
	args.RequireConsistent = true
	args.AllowStale = false
	if done, err := a.srv.forward("ACL.ReplicationStatus", args, args, reply); done {
		return err
	}

	// There's no ACL token required here since this doesn't leak any
	// sensitive information, and we don't want people to have to use
	// management tokens if they are querying this via a health check.

	// Poll the latest status.
	a.srv.aclReplicationStatusLock.RLock()
	*reply = a.srv.aclReplicationStatus
	a.srv.aclReplicationStatusLock.RUnlock()
	return nil
}
