// ClawEh
// License: MIT

package config

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestForumLimitsEffective(t *testing.T) {
	defaults := ForumLimitsConfig{MaxCalls: 200, MaxDurationSeconds: 7200, CallTimeoutSeconds: 1800, MaxParallelCalls: 8}
	tests := []struct {
		name string
		in   ForumLimitsConfig
		want ForumLimitsConfig
	}{
		{"absent", ForumLimitsConfig{}, defaults},
		{
			"all set",
			ForumLimitsConfig{MaxCalls: 50, MaxDurationSeconds: 60, CallTimeoutSeconds: 30, MaxParallelCalls: 2},
			ForumLimitsConfig{MaxCalls: 50, MaxDurationSeconds: 60, CallTimeoutSeconds: 30, MaxParallelCalls: 2},
		},
		{
			"one set",
			ForumLimitsConfig{MaxParallelCalls: 16},
			ForumLimitsConfig{MaxCalls: 200, MaxDurationSeconds: 7200, CallTimeoutSeconds: 1800, MaxParallelCalls: 16},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.in.Effective(); got != tt.want {
				t.Errorf("Effective() = %+v, want %+v", got, tt.want)
			}
		})
	}
}

func TestForumLimitsValidate(t *testing.T) {
	if err := (ForumLimitsConfig{}).Validate(); err != nil {
		t.Errorf("zero limits: %v", err)
	}
	err := ForumLimitsConfig{MaxCalls: -1, MaxParallelCalls: -3}.Validate()
	if err == nil {
		t.Fatal("negative limits accepted")
	}
	for _, want := range []string{
		"forum.limits.max_calls: must be 0 (the default) or more, got -1",
		"forum.limits.max_parallel_calls: must be 0 (the default) or more, got -3",
	} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q lacks %q", err, want)
		}
	}
}

func TestLoadConfig_ForumLimits(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.json")
	write := func(body string) {
		t.Helper()
		if err := os.WriteFile(configPath, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write(`{"forum":{"limits":{"max_calls":500,"call_timeout_seconds":0}}}`)
	cfg, err := LoadConfig(configPath)
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if got := cfg.Forum.Limits.Effective(); got.MaxCalls != 500 || got.CallTimeoutSeconds != DefaultForumCallTimeoutSeconds {
		t.Errorf("Effective() = %+v, want max_calls 500 and the default call timeout", got)
	}

	write(`{"forum":{"limits":{"max_duration_seconds":-5}}}`)
	if _, err := LoadConfig(configPath); err == nil || !strings.Contains(err.Error(), "forum.limits.max_duration_seconds") {
		t.Errorf("LoadConfig with a negative limit = %v", err)
	}
}

// The section round-trips through a save, and is left out of the file when
// nothing in it is set.
func TestStore_ForumLimits(t *testing.T) {
	s := newTestStore(t)
	data, err := os.ReadFile(s.Path())
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), `"forum"`) {
		t.Errorf("an unset forum section is written: %s", data)
	}

	if err = s.Update(func(c *Config) error {
		c.Forum.Limits = ForumLimitsConfig{MaxCalls: 300, MaxParallelCalls: 4}
		return nil
	}); err != nil {
		t.Fatalf("Update: %v", err)
	}
	if _, err = s.Reload(); err != nil {
		t.Fatalf("Reload: %v", err)
	}
	if got := s.Current().Forum.Limits; got != (ForumLimitsConfig{MaxCalls: 300, MaxParallelCalls: 4}) {
		t.Errorf("after save and reload = %+v", got)
	}

	err = s.Update(func(c *Config) error {
		c.Forum.Limits.CallTimeoutSeconds = -1
		return nil
	})
	var ve *ValidationError
	if !errors.As(err, &ve) || !strings.Contains(err.Error(), "forum.limits.call_timeout_seconds") {
		t.Errorf("Update with a negative limit = %v, want a ValidationError", err)
	}
	if got := s.Current().Forum.Limits.CallTimeoutSeconds; got != 0 {
		t.Errorf("a refused update was applied: call_timeout_seconds = %d", got)
	}
}
