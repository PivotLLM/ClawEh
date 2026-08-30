// ClawEh
// License: MIT

package telegram

import "testing"

func TestIsTransientPollError(t *testing.T) {
	cases := []struct {
		name string
		msg  string
		want bool
	}{
		// Real telego long-poll churn from the field (502/504 + retry notice).
		{"exec error 502", "Execution error getUpdates: request call: internal server error: 502", true},
		{"getting updates 502", "Getting updates: telego: getUpdates: internal execution: request call: internal server error: 502", true},
		{"retry notice", "Retrying getting updates in 2s...", true},
		{"exec error 504", "Execution error getUpdates: request call: internal server error: 504", true},
		{"503", "Getting updates: telego: getUpdates: request call: internal server error: 503", true},
		// Transport-level blips during getUpdates.
		{"conn reset", "Execution error getUpdates: request call: read: connection reset by peer", true},
		{"i/o timeout", "Execution error getUpdates: request call: dial tcp: i/o timeout", true},
		{"tls timeout", "Execution error getUpdates: request call: net/http: TLS handshake timeout", true},

		// Genuine faults must stay at ERROR.
		{"unauthorized 401", "Execution error getUpdates: request call: internal server error: 401", false},
		{"conflict 409", "Execution error getUpdates: request call: internal server error: 409", false},
		{"bad request 400", "Execution error getUpdates: request call: internal server error: 400", false},
		// 5xx outside the getUpdates path is not downgraded here.
		{"sendMessage 502", "Execution error sendMessage: request call: internal server error: 502", false},
		{"unrelated", "some other telego message", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := isTransientPollError(c.msg); got != c.want {
				t.Fatalf("isTransientPollError(%q) = %v, want %v", c.msg, got, c.want)
			}
		})
	}
}
