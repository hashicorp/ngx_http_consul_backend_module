// Copyright (c) HashiCorp, Inc.
// SPDX-License-Identifier: MPL-2.0

package pool

type RPCType byte

const (
	// keep numbers unique.
	// iota depends on order
	RPCConsul      RPCType = 0
	RPCRaft                = 1
	RPCMultiplex           = 2 // Old Muxado byte, no longer supported.
	RPCTLS                 = 3
	RPCMultiplexV2         = 4
	RPCSnapshot            = 5
	RPCGossip              = 6
)
