// ClawEh
// License: MIT

package global

import "testing"

func TestYesNo(t *testing.T) {
	truthy := []string{"on", "ON", " yes ", "y", "true", "t", "1", "enable", "enabled"}
	falsy := []string{"off", "no", "n", "false", "f", "0", "disable", "disabled"}
	for _, s := range truthy {
		v, err := YesNo(s)
		if err != nil || !v {
			t.Errorf("YesNo(%q) = %v,%v; want true,nil", s, v, err)
		}
	}
	for _, s := range falsy {
		v, err := YesNo(s)
		if err != nil || v {
			t.Errorf("YesNo(%q) = %v,%v; want false,nil", s, v, err)
		}
	}
	for _, s := range []string{"maybe", "", "2", "onoff"} {
		if _, err := YesNo(s); err == nil {
			t.Errorf("YesNo(%q) expected error", s)
		}
	}
}
