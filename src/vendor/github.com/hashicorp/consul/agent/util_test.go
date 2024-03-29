// Copyright (c) HashiCorp, Inc.
// SPDX-License-Identifier: MPL-2.0

package agent

import (
	"os"
	"runtime"
	"testing"
	"time"

	"github.com/hashicorp/consul/testutil"
)

func TestAEScale(t *testing.T) {
	t.Parallel()
	intv := time.Minute
	if v := aeScale(intv, 100); v != intv {
		t.Fatalf("Bad: %v", v)
	}
	if v := aeScale(intv, 200); v != 2*intv {
		t.Fatalf("Bad: %v", v)
	}
	if v := aeScale(intv, 1000); v != 4*intv {
		t.Fatalf("Bad: %v", v)
	}
	if v := aeScale(intv, 10000); v != 8*intv {
		t.Fatalf("Bad: %v", v)
	}
}

func TestStringHash(t *testing.T) {
	t.Parallel()
	in := "hello world"
	expected := "5eb63bbbe01eeed093cb22bb8f5acdc3"

	if out := stringHash(in); out != expected {
		t.Fatalf("bad: %s", out)
	}
}

func TestSetFilePermissions(t *testing.T) {
	t.Parallel()
	if runtime.GOOS == "windows" {
		t.SkipNow()
	}
	tempFile := testutil.TempFile(t, "consul")
	path := tempFile.Name()
	defer os.Remove(path)

	// Bad UID fails
	if err := setFilePermissions(path, "%", "", ""); err == nil {
		t.Fatalf("should fail")
	}

	// Bad GID fails
	if err := setFilePermissions(path, "", "%", ""); err == nil {
		t.Fatalf("should fail")
	}

	// Bad mode fails
	if err := setFilePermissions(path, "", "", "%"); err == nil {
		t.Fatalf("should fail")
	}

	// Allows omitting user/group/mode
	if err := setFilePermissions(path, "", "", ""); err != nil {
		t.Fatalf("err: %s", err)
	}

	// Doesn't change mode if not given
	if err := os.Chmod(path, 0700); err != nil {
		t.Fatalf("err: %s", err)
	}
	if err := setFilePermissions(path, "", "", ""); err != nil {
		t.Fatalf("err: %s", err)
	}
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatalf("err: %s", err)
	}
	if fi.Mode().String() != "-rwx------" {
		t.Fatalf("bad: %s", fi.Mode())
	}

	// Changes mode if given
	if err := setFilePermissions(path, "", "", "0777"); err != nil {
		t.Fatalf("err: %s", err)
	}
	fi, err = os.Stat(path)
	if err != nil {
		t.Fatalf("err: %s", err)
	}
	if fi.Mode().String() != "-rwxrwxrwx" {
		t.Fatalf("bad: %s", fi.Mode())
	}
}
