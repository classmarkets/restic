package main

import (
	"testing"

	rtest "github.com/classmarkets/restic/internal/test"
)

func TestSplitPath(t *testing.T) {
	cases := []struct {
		given string
		want  []string
	}{
		{"", []string{""}},
		{".", []string{"."}},
		{"/", []string{"/"}},
		{"/foo", []string{"/", "foo"}},
		{"/foo/bar", []string{"/", "foo", "bar"}},
		{"/foo/bar/baz", []string{"/", "foo", "bar", "baz"}},
		{"foo/bar", []string{"foo", "bar"}},
		{"foo/bar/baz", []string{"foo", "bar", "baz"}},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.given, func(t *testing.T) {
			rtest.Equals(t, tc.want, splitPath(tc.given))
		})
	}
}
