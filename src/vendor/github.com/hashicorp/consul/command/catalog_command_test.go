// Copyright (c) HashiCorp, Inc.
// SPDX-License-Identifier: MPL-2.0

package command

import "testing"

func TestCatalogCommand_noTabs(t *testing.T) {
	t.Parallel()
	assertNoTabs(t, new(CatalogCommand))
}
