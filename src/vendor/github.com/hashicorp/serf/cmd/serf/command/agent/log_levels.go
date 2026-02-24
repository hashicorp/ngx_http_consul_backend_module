// Copyright IBM Corp. 2017, 2023
// SPDX-License-Identifier: MPL-2.0

package agent

import (
	"github.com/hashicorp/logutils"
	"io/ioutil"
)

// LevelFilter returns a LevelFilter that is configured with the log
// levels that we use.
func LevelFilter() *logutils.LevelFilter {
	return &logutils.LevelFilter{
		Levels:   []logutils.LogLevel{"TRACE", "DEBUG", "INFO", "WARN", "ERR"},
		MinLevel: "INFO",
		Writer:   ioutil.Discard,
	}
}

// ValidateLevelFilter verifies that the log levels within the filter
// are valid.
func ValidateLevelFilter(minLevel logutils.LogLevel, filter *logutils.LevelFilter) bool {
	for _, level := range filter.Levels {
		if level == minLevel {
			return true
		}
	}
	return false
}
