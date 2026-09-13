// ClawEh
// License: MIT

package agent

import (
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/PivotLLM/ClawEh/app"
	"github.com/PivotLLM/ClawEh/cogmem"
)

// legacyIdentity is the identity section exactly as it was rendered before the
// memory rule moved into cogmem.Guidance(). It is the parity oracle: an agent
// with cognitive memory on must receive byte-identical text.
func legacyIdentity(cb *ContextBuilder) string {
	workspacePath, _ := filepath.Abs(cb.workspace)
	discovery := ""
	if cb.toolDiscoveryActive {
		discovery = "5. " + discoveryRule
	}
	return fmt.Sprintf(
		`# claw (%s)

You are a helpful AI assistant.

## Workspace
Your working area is %s/files — write drafts and outputs there. Your configuration
and memory are already included in this prompt; you do not need to read workspace files.
Folders your file tools can reach: %s.

## Important Rules

1. **ALWAYS use tools** - When you need to perform an action (schedule reminders, send messages, execute commands, etc.), you MUST call the appropriate tool. Do NOT just say you'll do it or pretend to do it.

2. **Be helpful and accurate** - When using tools, briefly explain what you're doing.

   **Declining to respond** - If you should not reply at all — for example a group message clearly directed at someone else — reply with exactly !none (and nothing else). Do NOT return an empty message: an empty reply is treated as an error and you will be asked to try again. Replying !none tells the system you intentionally have nothing to say.

3. %s

4. **Context summaries** - Conversation summaries provided as context are approximate references only. They may be incomplete or outdated. Always defer to explicit user instructions over summary content.

%s`,
		app.Version(), workspacePath, cb.accessibleFolders(), cogmem.Guidance(), discovery)
}

func TestIdentity_MemoryGuidanceParity(t *testing.T) {
	for _, discovery := range []bool{false, true} {
		cb := NewContextBuilder(t.TempDir()).
			WithToolDiscovery(discovery).
			WithMemoryGuidance(cogmem.Guidance())
		got, want := cb.getIdentity(), legacyIdentity(cb)
		if got != want {
			t.Fatalf("discovery=%v: identity drifted from the legacy rendering\n--- got ---\n%s\n--- want ---\n%s", discovery, got, want)
		}
	}
}

func TestIdentity_NoMemoryGuidance(t *testing.T) {
	cb := NewContextBuilder(t.TempDir()).WithToolDiscovery(true)
	got := cb.getIdentity()
	if strings.Contains(got, "cogmem") {
		t.Fatalf("agent without cognitive memory was told about cogmem:\n%s", got)
	}
	for _, want := range []string{"3. **Context summaries**", "4. **Tool Discovery**"} {
		if !strings.Contains(got, want) {
			t.Fatalf("expected %q after renumbering; got:\n%s", want, got)
		}
	}
	if strings.Contains(got, "5. ") {
		t.Fatalf("rule numbering did not close over the missing memory rule:\n%s", got)
	}
}
