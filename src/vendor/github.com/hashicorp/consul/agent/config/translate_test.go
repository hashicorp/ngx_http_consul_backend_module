// Copyright (c) HashiCorp, Inc.
// SPDX-License-Identifier: MPL-2.0

package config

import (
	"encoding/json"
	"testing"

	"github.com/pascaldekloe/goe/verify"
)

func TestTranslateKeys(t *testing.T) {
	fromJSON := func(s string) map[string]interface{} {
		var m map[string]interface{}
		if err := json.Unmarshal([]byte(s), &m); err != nil {
			t.Fatal(err)
		}
		return m
	}

	tests := []struct {
		desc string
		in   map[string]interface{}
		out  map[string]interface{}
		dict map[string]string
	}{
		{
			desc: "x->y",
			in:   map[string]interface{}{"a": "aa", "x": "xx"},
			out:  map[string]interface{}{"a": "aa", "y": "xx"},
			dict: map[string]string{"x": "y"},
		},
		{
			desc: "discard x",
			in:   map[string]interface{}{"a": "aa", "x": "xx", "y": "yy"},
			out:  map[string]interface{}{"a": "aa", "y": "yy"},
			dict: map[string]string{"x": "y"},
		},
		{
			desc: "b.x->b.y",
			in:   map[string]interface{}{"a": "aa", "b": map[string]interface{}{"x": "xx"}},
			out:  map[string]interface{}{"a": "aa", "b": map[string]interface{}{"y": "xx"}},
			dict: map[string]string{"x": "y"},
		},
		{
			desc: "json: x->y",
			in:   fromJSON(`{"a":"aa","x":"xx"}`),
			out:  fromJSON(`{"a":"aa","y":"xx"}`),
			dict: map[string]string{"x": "y"},
		},
		{
			desc: "json: X->y",
			in:   fromJSON(`{"a":"aa","X":"xx"}`),
			out:  fromJSON(`{"a":"aa","y":"xx"}`),
			dict: map[string]string{"x": "y"},
		},
		{
			desc: "json: discard x",
			in:   fromJSON(`{"a":"aa","x":"xx","y":"yy"}`),
			out:  fromJSON(`{"a":"aa","y":"yy"}`),
			dict: map[string]string{"x": "y"},
		},
		{
			desc: "json: b.x->b.y",
			in:   fromJSON(`{"a":"aa","b":{"x":"xx"}}`),
			out:  fromJSON(`{"a":"aa","b":{"y":"xx"}}`),
			dict: map[string]string{"x": "y"},
		},
		{
			desc: "json: b[0].x->b[0].y",
			in:   fromJSON(`{"a":"aa","b":[{"x":"xx"}]}`),
			out:  fromJSON(`{"a":"aa","b":[{"y":"xx"}]}`),
			dict: map[string]string{"x": "y"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.desc, func(t *testing.T) {
			TranslateKeys(tt.in, tt.dict)
			if got, want := tt.in, tt.out; !verify.Values(t, "", got, want) {
				t.Fail()
			}
		})
	}
}
