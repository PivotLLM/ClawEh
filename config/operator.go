// ClawEh
// License: MIT

package config

import (
	"errors"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

// MaxOperatorNameLen is the longest operator.name, in characters.
const MaxOperatorNameLen = 64

// OperatorConfig describes the person operating this install.
type OperatorConfig struct {
	// Name is the name used for the operator's own messages when nothing
	// better is known: a device with no user of its own, and the web chat.
	// Empty uses the admin account's username.
	Name string `json:"name,omitempty"`
}

// Validate refuses a name (after trimming) longer than MaxOperatorNameLen
// characters, not valid UTF-8, or containing a control character.
func (o OperatorConfig) Validate() error {
	name := strings.TrimSpace(o.Name)
	if !utf8.ValidString(name) || utf8.RuneCountInString(name) > MaxOperatorNameLen ||
		strings.IndexFunc(name, unicode.IsControl) >= 0 {
		return errors.New("Your name must be at most " + strconv.Itoa(MaxOperatorNameLen) + " characters, with no control characters.")
	}
	return nil
}

// OperatorName is operator.name trimmed, or "" when it is not set.
func (c *Config) OperatorName() string {
	return strings.TrimSpace(c.Operator.Name)
}
