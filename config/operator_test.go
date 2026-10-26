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

const operatorNameRefusal = "Your name must be at most 64 characters, with no control characters."

// Validate accepts a name of up to MaxOperatorNameLen characters after
// trimming, and refuses a longer one or one with a control character.
func TestOperatorConfig_Validate(t *testing.T) {
	for _, tc := range []struct {
		name, value string
		ok          bool
	}{
		{"empty", "", true},
		{"plain", "Alice", true},
		{"surrounding spaces", "  Alice  ", true},
		{"64 characters", strings.Repeat("a", MaxOperatorNameLen), true},
		{"64 characters plus spaces", " " + strings.Repeat("é", MaxOperatorNameLen) + " ", true},
		{"65 characters", strings.Repeat("a", MaxOperatorNameLen+1), false},
		{"control character", "Al\x07ice", false},
		{"inner newline", "Alice\nBob", false},
		{"invalid UTF-8", "Al\xffice", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := OperatorConfig{Name: tc.value}.Validate()
			if tc.ok && err != nil {
				t.Fatalf("Validate(%q) = %v, want accepted", tc.value, err)
			}
			if !tc.ok && (err == nil || err.Error() != operatorNameRefusal) {
				t.Fatalf("Validate(%q) = %v, want %q", tc.value, err, operatorNameRefusal)
			}
		})
	}
}

// OperatorName is the trimmed operator.name.
func TestConfig_OperatorName(t *testing.T) {
	c := &Config{Operator: OperatorConfig{Name: "  Alice "}}
	if got := c.OperatorName(); got != "Alice" {
		t.Fatalf("OperatorName() = %q, want Alice", got)
	}
}

// operator.name round-trips through a save, is left out of the file when
// unset, and a refused one is refused by Update and by LoadConfig.
func TestStore_OperatorName(t *testing.T) {
	s := newTestStore(t)
	data, err := os.ReadFile(s.Path())
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), `"operator"`) {
		t.Errorf("an unset operator section is written: %s", data)
	}

	if err = s.Update(func(c *Config) error {
		c.Operator.Name = "Alice"
		return nil
	}); err != nil {
		t.Fatalf("Update: %v", err)
	}
	if _, err = s.Reload(); err != nil {
		t.Fatalf("Reload: %v", err)
	}
	if got := s.Current().OperatorName(); got != "Alice" {
		t.Errorf("after save and reload operator.name = %q, want Alice", got)
	}

	err = s.Update(func(c *Config) error {
		c.Operator.Name = strings.Repeat("a", MaxOperatorNameLen+1)
		return nil
	})
	var ve *ValidationError
	if !errors.As(err, &ve) || err.Error() != operatorNameRefusal {
		t.Errorf("Update with a 65-character name = %v, want a ValidationError", err)
	}
	if got := s.Current().OperatorName(); got != "Alice" {
		t.Errorf("a refused update was applied: operator.name = %q", got)
	}

	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, []byte(`{"operator":{"name":"Al\u0007ice"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadConfig(path); err == nil || !strings.Contains(err.Error(), operatorNameRefusal) {
		t.Errorf("LoadConfig with a control character = %v, want %q", err, operatorNameRefusal)
	}
}
