// Copyright (c) HashiCorp, Inc.
// SPDX-License-Identifier: MPL-2.0

// Package rootcerts contains functions to aid in loading CA certificates for
// TLS connections.
//
// In addition, its default behavior on Darwin works around an open issue [1]
// in Go's crypto/x509 that prevents certicates from being loaded from the
// System or Login keychains.
//
// [1] https://github.com/golang/go/issues/14514
package rootcerts
