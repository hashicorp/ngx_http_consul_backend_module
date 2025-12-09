// Copyright IBM Corp. 2017, 2023
// SPDX-License-Identifier: MPL-2.0

package lib

// StrContains checks if a list contains a string
func StrContains(l []string, s string) bool {
	for _, v := range l {
		if v == s {
			return true
		}
	}
	return false
}
