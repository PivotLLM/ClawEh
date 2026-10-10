// ClawEh
// License: MIT

package config

import (
	"errors"
	"fmt"
)

// ForumConfig holds the install-wide forum settings.
type ForumConfig struct {
	Limits ForumLimitsConfig `json:"limits,omitzero"`
}

// ForumLimitsConfig is the install's maximum for four of a forum's limits:
// forum_validate and forum_launch refuse a forum configuration (or a layer's
// max_calls) above them. A field left out or set to 0 uses its default;
// a negative value is refused at load and on save.
type ForumLimitsConfig struct {
	MaxCalls           int `json:"max_calls,omitempty"`
	MaxDurationSeconds int `json:"max_duration_seconds,omitempty"`
	CallTimeoutSeconds int `json:"call_timeout_seconds,omitempty"`
	MaxParallelCalls   int `json:"max_parallel_calls,omitempty"`
}

// The defaults of forum.limits.
const (
	DefaultForumMaxCalls           = 200
	DefaultForumMaxDurationSeconds = 7200
	DefaultForumCallTimeoutSeconds = 1800
	DefaultForumMaxParallelCalls   = 8
)

// Effective returns the limits with every unset (0) field at its default.
func (l ForumLimitsConfig) Effective() ForumLimitsConfig {
	orDefault := func(v, def int) int {
		if v <= 0 {
			return def
		}
		return v
	}
	return ForumLimitsConfig{
		MaxCalls:           orDefault(l.MaxCalls, DefaultForumMaxCalls),
		MaxDurationSeconds: orDefault(l.MaxDurationSeconds, DefaultForumMaxDurationSeconds),
		CallTimeoutSeconds: orDefault(l.CallTimeoutSeconds, DefaultForumCallTimeoutSeconds),
		MaxParallelCalls:   orDefault(l.MaxParallelCalls, DefaultForumMaxParallelCalls),
	}
}

// Validate refuses a negative value, naming every one.
func (l ForumLimitsConfig) Validate() error {
	var errs []error
	for _, f := range []struct {
		name  string
		value int
	}{
		{"max_calls", l.MaxCalls},
		{"max_duration_seconds", l.MaxDurationSeconds},
		{"call_timeout_seconds", l.CallTimeoutSeconds},
		{"max_parallel_calls", l.MaxParallelCalls},
	} {
		if f.value < 0 {
			errs = append(errs, fmt.Errorf("forum.limits.%s: must be 0 (the default) or more, got %d", f.name, f.value))
		}
	}
	return errors.Join(errs...)
}
