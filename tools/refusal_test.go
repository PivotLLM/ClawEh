package tools

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/PivotLLM/ClawEh/logger"
)

// externalRefusalError is how a package that does not import tools marks a refusal.
type externalRefusalError struct{ error }

func (externalRefusalError) Refusal() bool { return true }

func TestIsRefusal(t *testing.T) {
	base := errors.New("no")
	for _, tc := range []struct {
		name string
		err  error
		want bool
	}{
		{"nil", nil, false},
		{"plain error", base, false},
		{"marked", Refusal(base), true},
		{"marked and wrapped", fmt.Errorf("call: %w", Refusal(base)), true},
		{"marked by another package", externalRefusalError{base}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := IsExpectedRefusal(tc.err); got != tc.want {
				t.Fatalf("IsExpectedRefusal = %v, want %v", got, tc.want)
			}
		})
	}
	if !errors.Is(Refusal(base), base) {
		t.Fatal("Refusal must wrap its error")
	}
}

// An expected refusal is logged as a warning; a genuine failure as an error.
func TestRegistry_RefusalLoggedAtWarn(t *testing.T) {
	for _, tc := range []struct {
		name      string
		result    *ToolResult
		wantLevel string
		wantMsg   string
	}{
		{"refusal", ErrorResult("You may not message Bob.").WithError(Refusal(errors.New("not allowed"))), `"level":"warn"`, "Tool call refused"},
		{"failure", ErrorResult("disk full").WithError(errors.New("disk full")), `"level":"error"`, "Tool execution failed"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var buf safeBuf
			restore := logger.RedirectForTest(&buf)
			defer restore()

			r := NewToolRegistry()
			r.Register(&mockRegistryTool{name: "probe", params: map[string]any{}, result: tc.result})
			r.ExecuteWithContext(context.Background(), "probe", nil, "", "", nil)

			var line string
			for l := range strings.SplitSeq(buf.String(), "\n") {
				if strings.Contains(l, tc.wantMsg) {
					line = l
				}
			}
			if line == "" || !strings.Contains(line, tc.wantLevel) {
				t.Fatalf("want %q at %s, got log:\n%s", tc.wantMsg, tc.wantLevel, buf.String())
			}
		})
	}
}
