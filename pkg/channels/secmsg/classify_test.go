package secmsg

import (
	"errors"
	"testing"

	"github.com/PivotLLM/ClawEh/pkg/channels"

	"github.com/tenebris-tech/secmsg/schema"
)

func TestSendErrForCode(t *testing.T) {
	base := errors.New("underlying")

	tests := []struct {
		name    string
		code    int
		hasCode bool
		want    error
	}{
		{"stealth → receive-only", schema.ErrCodeStealth, true, channels.ErrReceiveOnly},
		{"other rpc code → send failed", -32000, true, channels.ErrSendFailed},
		{"no code (transport error) → send failed", 0, false, channels.ErrSendFailed},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := sendErrForCode(base, tt.code, tt.hasCode)
			if !errors.Is(got, tt.want) {
				t.Errorf("sendErrForCode() = %v, want wrap of %v", got, tt.want)
			}
			// The underlying error must be preserved in the message for diagnostics.
			if !errors.Is(got, tt.want) || got.Error() == tt.want.Error() {
				t.Errorf("expected underlying detail preserved, got %q", got.Error())
			}
		})
	}
}
