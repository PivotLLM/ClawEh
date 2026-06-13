package agent

import (
	"reflect"
	"testing"
)

func TestResolveCompressModelChain(t *testing.T) {
	tests := []struct {
		name   string
		agent  []string
		global []string
		want   []string
	}{
		{"empty", nil, nil, nil},
		{"global only", nil, []string{"g1", "g2"}, []string{"g1", "g2"}},
		{"agent only", []string{"a1"}, nil, []string{"a1"}},
		{
			"agent first then global",
			[]string{"a1", "a2"}, []string{"g1", "g2"},
			[]string{"a1", "a2", "g1", "g2"},
		},
		{
			"dedup keeps first occurrence and order",
			[]string{"x", "a1"}, []string{"g1", "x", "g2"},
			[]string{"x", "a1", "g1", "g2"},
		},
		{
			"blank entries skipped",
			[]string{"", " a1 "}, []string{" ", "g1"},
			[]string{"a1", "g1"},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := resolveCompressModelChain(tc.agent, tc.global)
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("resolveCompressModelChain(%v,%v)=%v, want %v", tc.agent, tc.global, got, tc.want)
			}
		})
	}
}
