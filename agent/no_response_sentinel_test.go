package agent

import "testing"

func TestIsNoResponseSentinel(t *testing.T) {
	match := []string{
		"!none",
		"!NONE",
		"  !none  ",
		"\"!none\"",
		"`!none`",
		"'!none'",
		"\"!none\".",
		"!none.",
		"!none — that's for Bob",
		"!none (this message is addressed to Alice)",
		"  `!none`  ",
	}
	for _, s := range match {
		if !isNoResponseSentinel(s) {
			t.Errorf("expected %q to be the no-response sentinel", s)
		}
	}

	noMatch := []string{
		"",
		"none",
		"!nonexistent",
		"!noneworthy",
		"sure, here is my reply",
		"the answer is !none-ish",
		"I will !none this",
	}
	for _, s := range noMatch {
		if isNoResponseSentinel(s) {
			t.Errorf("did NOT expect %q to be the no-response sentinel", s)
		}
	}
}
