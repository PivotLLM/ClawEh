// ClawEh
// License: MIT

package config

import (
	"errors"
	"testing"
)

// newProblems reports only problems before did not have, and skips those
// toErr never refuses.
func TestNewProblems(t *testing.T) {
	key := func(s string) string { return s }
	toErr := func(s string) error {
		if s == "tolerated" {
			return nil
		}
		return errors.New(s)
	}
	errs := newProblems([]string{"old"}, []string{"old", "new", "tolerated"}, key, toErr)
	if len(errs) != 1 || errs[0].Error() != "new" {
		t.Fatalf("errs = %v, want [new]", errs)
	}
	if errs := newProblems(nil, nil, key, toErr); errs != nil {
		t.Fatalf("errs = %v, want none", errs)
	}
}
