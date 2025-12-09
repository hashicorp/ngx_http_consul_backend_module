// Copyright IBM Corp. 2017, 2023
// SPDX-License-Identifier: MPL-2.0

package lib

import (
	"os"
	"path/filepath"
)

// EnsurePath is used to make sure a path exists
func EnsurePath(path string, dir bool) error {
	if !dir {
		path = filepath.Dir(path)
	}
	return os.MkdirAll(path, 0755)
}
