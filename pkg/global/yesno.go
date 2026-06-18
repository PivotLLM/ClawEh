// ClawEh
// License: MIT

package global

import (
	"fmt"
	"strings"
)

// YesNo parses a human on/off answer into a bool. It accepts (case- and
// whitespace-insensitive): on/off, yes/no, y/n, true/false, t/f, 1/0,
// enable/disable, enabled/disabled. Returns an error for anything else so
// callers can show usage rather than guess.
func YesNo(s string) (bool, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "on", "yes", "y", "true", "t", "1", "enable", "enabled":
		return true, nil
	case "off", "no", "n", "false", "f", "0", "disable", "disabled":
		return false, nil
	default:
		return false, fmt.Errorf("expected on/off (got %q)", s)
	}
}
