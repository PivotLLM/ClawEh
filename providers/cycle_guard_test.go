// ClawEh
// License: MIT

package providers_test

import (
	"os/exec"
	"strings"
	"testing"
)

// TestSpawnllmImportsNoClawEh enforces the architectural invariant that the
// extracted dispatch module never imports a host: spawnllm must depend only on
// toolspec + stdlib (+ provider SDKs). If this fails, a ClawEh import crept into
// spawnllm and the module dependency graph is at risk of a cycle.
func TestSpawnllmImportsNoClawEh(t *testing.T) {
	out, err := exec.Command("go", "list", "-deps", "github.com/PivotLLM/spawnllm/...").CombinedOutput()
	if err != nil {
		t.Skipf("go list unavailable: %v\n%s", err, out)
	}
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "github.com/PivotLLM/ClawEh") {
			t.Fatalf("spawnllm must not import a host, but depends on %q", line)
		}
	}
}
