// Copyright (c) HashiCorp, Inc.
// SPDX-License-Identifier: MPL-2.0

package consul

import (
	"net"
	"net/rpc"
	"os"
	"testing"
	"time"

	"github.com/hashicorp/consul/agent/pool"
	"github.com/hashicorp/consul/testrpc"
	"github.com/hashicorp/net-rpc-msgpackrpc"
)

func rpcClient(t *testing.T, s *Server) rpc.ClientCodec {
	addr := s.config.RPCAdvertise
	conn, err := net.DialTimeout("tcp", addr.String(), time.Second)
	if err != nil {
		t.Fatalf("err: %v", err)
	}

	// Write the Consul RPC byte to set the mode
	conn.Write([]byte{byte(pool.RPCConsul)})
	return msgpackrpc.NewClientCodec(conn)
}

func TestStatusLeader(t *testing.T) {
	t.Parallel()
	dir1, s1 := testServer(t)
	defer os.RemoveAll(dir1)
	defer s1.Shutdown()
	codec := rpcClient(t, s1)
	defer codec.Close()

	arg := struct{}{}
	var leader string
	if err := msgpackrpc.CallWithCodec(codec, "Status.Leader", arg, &leader); err != nil {
		t.Fatalf("err: %v", err)
	}
	if leader != "" {
		t.Fatalf("unexpected leader: %v", leader)
	}

	testrpc.WaitForLeader(t, s1.RPC, "dc1")

	if err := msgpackrpc.CallWithCodec(codec, "Status.Leader", arg, &leader); err != nil {
		t.Fatalf("err: %v", err)
	}
	if leader == "" {
		t.Fatalf("no leader")
	}
}

func TestStatusPeers(t *testing.T) {
	t.Parallel()
	dir1, s1 := testServer(t)
	defer os.RemoveAll(dir1)
	defer s1.Shutdown()
	codec := rpcClient(t, s1)
	defer codec.Close()

	arg := struct{}{}
	var peers []string
	if err := msgpackrpc.CallWithCodec(codec, "Status.Peers", arg, &peers); err != nil {
		t.Fatalf("err: %v", err)
	}
	if len(peers) != 1 {
		t.Fatalf("no peers: %v", peers)
	}
}
