// ClawEh
// License: MIT

package agent

import (
	"os"
	"strings"
	"testing"
	"time"
)

// atDate returns a ContextBuilder whose date line is pinned to a fixed clock.
func atDate(t *testing.T, workspace string, clock *time.Time) *ContextBuilder {
	t.Helper()
	cb := NewContextBuilder(workspace)
	cb.now = func() time.Time { return *clock }
	return cb
}

// TestDateAnchor_RollsOverAtMidnight is the guard the static-prompt cache needed
// before a date could live in it. The cache keys on file mtimes, and no file
// changes when the day does — so without an explicit date check the prompt would
// serve yesterday's date indefinitely. That is a confidently wrong answer, which
// is the exact failure the anchor exists to prevent.
func TestDateAnchor_RollsOverAtMidnight(t *testing.T) {
	dir := setupWorkspace(t, map[string]string{"IDENTITY.md": "# Identity"})
	defer os.RemoveAll(dir)

	clock := time.Date(2026, 2, 12, 23, 59, 0, 0, time.UTC) // Thursday
	cb := atDate(t, dir, &clock)

	first := cb.BuildSystemPromptWithCache()
	if !strings.Contains(first, "Today is Thursday, Feb 12, 2026") {
		t.Fatalf("expected Feb 12 in the prompt, got:\n%s", tailOf(first))
	}

	// Same day, later time: the cache must hold (no file changed).
	clock = clock.Add(30 * time.Second)
	if again := cb.BuildSystemPromptWithCache(); again != first {
		t.Error("prompt rebuilt within the same day; the cached prefix should be byte-identical")
	}

	// Past midnight: the cache must rebuild with the new date.
	clock = time.Date(2026, 2, 13, 0, 1, 0, 0, time.UTC) // Friday
	next := cb.BuildSystemPromptWithCache()
	if !strings.Contains(next, "Today is Friday, Feb 13, 2026") {
		t.Fatalf("date did not roll over, got:\n%s", tailOf(next))
	}
}

// TestDateAnchor_StableWithinTheDay is the property the whole change exists for:
// two builds at different times of the same day must be byte-identical, because
// any difference here invalidates the cached prefix for the entire conversation
// history behind it.
func TestDateAnchor_StableWithinTheDay(t *testing.T) {
	dir := setupWorkspace(t, map[string]string{"IDENTITY.md": "# Identity"})
	defer os.RemoveAll(dir)

	clock := time.Date(2026, 2, 12, 8, 0, 0, 0, time.UTC)
	cb := atDate(t, dir, &clock)
	morning := cb.BuildSystemPrompt()

	clock = time.Date(2026, 2, 12, 22, 45, 13, 0, time.UTC)
	evening := cb.BuildSystemPrompt()

	if morning != evening {
		t.Errorf("system prompt differs across the same day — the cached prefix would break every turn:\nmorning tail: %s\nevening tail: %s",
			tailOf(morning), tailOf(evening))
	}
}

// TestBuildMessages_NoPerTurnVolatility walks the assembled system message for
// anything that changes between two turns seconds apart. Whatever is found here
// costs the cache the whole conversation history, not just itself.
func TestBuildMessages_NoPerTurnVolatility(t *testing.T) {
	dir := setupWorkspace(t, map[string]string{"IDENTITY.md": "# Identity"})
	defer os.RemoveAll(dir)

	clock := time.Date(2026, 2, 12, 8, 0, 0, 0, time.UTC)
	cb := atDate(t, dir, &clock)

	first := cb.BuildMessages(nil, "", "hello", nil, "webui", "chat1")[0].Content
	clock = clock.Add(90 * time.Second)
	second := cb.BuildMessages(nil, "", "hello again", nil, "webui", "chat1")[0].Content

	if first != second {
		t.Errorf("system message changed between turns 90s apart:\n--- first ---\n%s\n--- second ---\n%s", first, second)
	}
}

// TestInvalidateCache_ClearsDate keeps the explicit invalidation path honest: a
// cleared cache must not leave a stale date behind that suppresses the next
// rebuild's date check.
func TestInvalidateCache_ClearsDate(t *testing.T) {
	dir := setupWorkspace(t, map[string]string{"IDENTITY.md": "# Identity"})
	defer os.RemoveAll(dir)

	clock := time.Date(2026, 2, 12, 8, 0, 0, 0, time.UTC)
	cb := atDate(t, dir, &clock)
	cb.BuildSystemPromptWithCache()

	cb.InvalidateCache()

	cb.systemPromptMutex.RLock()
	date := cb.cachedDate
	cb.systemPromptMutex.RUnlock()
	if date != "" {
		t.Errorf("cachedDate = %q after InvalidateCache, want empty", date)
	}
}

// tailOf returns the last 200 characters of s, where the date line lives.
func tailOf(s string) string {
	if len(s) <= 200 {
		return s
	}
	return "…" + s[len(s)-200:]
}
