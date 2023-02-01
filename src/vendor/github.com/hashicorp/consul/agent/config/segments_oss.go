// Copyright (c) HashiCorp, Inc.
// SPDX-License-Identifier: MPL-2.0

// +build !ent

package config

import (
	"github.com/hashicorp/consul/agent/structs"
)

func (b *Builder) validateSegments(rt RuntimeConfig) error {
	if rt.SegmentName != "" {
		return structs.ErrSegmentsNotSupported
	}
	if len(rt.Segments) > 0 {
		return structs.ErrSegmentsNotSupported
	}
	return nil
}
