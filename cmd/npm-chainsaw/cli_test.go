package main

import (
	"reflect"
	"testing"
)

func TestSplitFlagsAndPositionals(t *testing.T) {
	cases := []struct {
		name              string
		in                []string
		wantFlags, wantPos []string
	}{
		{
			name:      "flags before positionals",
			in:        []string{"--verbose", "--json", "list.txt"},
			wantFlags: []string{"--verbose", "--json"},
			wantPos:   []string{"list.txt"},
		},
		{
			name:      "flags after positionals (the bug this fixes)",
			in:        []string{"list.txt", "--verbose"},
			wantFlags: []string{"--verbose"},
			wantPos:   []string{"list.txt"},
		},
		{
			name:      "interleaved",
			in:        []string{"list.txt", "--json", "/tmp", "--verbose"},
			wantFlags: []string{"--json", "--verbose"},
			wantPos:   []string{"list.txt", "/tmp"},
		},
		{
			name:      "empty",
			in:        nil,
			wantFlags: nil,
			wantPos:   nil,
		},
		{
			name:      "lone hyphen treated as positional",
			in:        []string{"-", "list.txt"},
			wantFlags: nil,
			wantPos:   []string{"-", "list.txt"},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			gotFlags, gotPos := splitFlagsAndPositionals(c.in)
			if !reflect.DeepEqual(gotFlags, c.wantFlags) {
				t.Errorf("flags = %v, want %v", gotFlags, c.wantFlags)
			}
			if !reflect.DeepEqual(gotPos, c.wantPos) {
				t.Errorf("positional = %v, want %v", gotPos, c.wantPos)
			}
		})
	}
}
