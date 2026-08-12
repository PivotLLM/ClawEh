// ClawEh
// License: MIT

package agent

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/PivotLLM/ClawEh/pkg/media"
	"github.com/PivotLLM/ClawEh/pkg/providers"
)

func TestMediaRefsIn(t *testing.T) {
	refs := mediaRefsIn([]string{
		"media://11111111-1111-1111-1111-111111111111",
		"data:image/png;base64,AAA",
		"media://22222222-2222-2222-2222-222222222222",
		"/tmp/not-a-ref.png",
	})
	if len(refs) != 2 {
		t.Fatalf("expected 2 refs, got %v", refs)
	}
	if refs[0] != "media://11111111-1111-1111-1111-111111111111" ||
		refs[1] != "media://22222222-2222-2222-2222-222222222222" {
		t.Errorf("unexpected refs: %v", refs)
	}
}

func TestAppendMediaRefMarker(t *testing.T) {
	got := appendMediaRefMarker("look at this", []string{"media://abc"})
	want := "look at this\n[attachment ref(s): media://abc]"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
	// Empty content: marker only, no leading newline.
	got = appendMediaRefMarker("", []string{"media://abc"})
	if got != "[attachment ref(s): media://abc]" {
		t.Errorf("got %q", got)
	}
}

func TestCollectMediaRefs(t *testing.T) {
	msgs := []providers.Message{
		{Role: "user", Content: "hi", Media: []string{"media://11111111-1111-1111-1111-111111111111"}},
		// Flow B folded message: ref lives only in the text marker.
		{Role: "user", Content: "described\n[attachment ref(s): media://22222222-2222-2222-2222-222222222222]"},
		// Duplicate ref in both places must not repeat.
		{Role: "user", Content: "[attachment ref(s): media://11111111-1111-1111-1111-111111111111]"},
		{Role: "assistant", Content: "no refs here"},
	}
	refs := collectMediaRefs(msgs)
	if len(refs) != 2 {
		t.Fatalf("expected 2 unique refs, got %v", refs)
	}
}

// End-to-end pin reconcile through the AgentLoop helpers: refs present in the
// context stay pinned, dropped refs become reapable, release drops everything.
func TestSessionPinLifecycle(t *testing.T) {
	dir := t.TempDir()
	mkFile := func(name string) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
		return p
	}
	store := media.NewFileMediaStore()
	refA, err := store.Store(mkFile("a.jpg"), media.MediaMeta{Source: "test"}, "s")
	if err != nil {
		t.Fatal(err)
	}
	refB, err := store.Store(mkFile("b.jpg"), media.MediaMeta{Source: "test"}, "s")
	if err != nil {
		t.Fatal(err)
	}

	al := &AgentLoop{mediaStore: store}
	const session = "agent:alice:main"

	al.pinSessionMediaRefs(session, []string{refA, refB})

	// Context now only references refA (refB compacted away) → reconcile unpins refB.
	al.reconcileSessionPins(session, []providers.Message{
		{Role: "user", Content: "[attachment ref(s): " + refA + "]"},
	})

	pinner := al.refPinner()
	if pinner == nil {
		t.Fatal("expected store to implement RefPinner")
	}
	// Verify via a second owner probe: re-pinning refB must still work (it exists),
	// and releasing the session leaves no pins behind.
	if err := pinner.Pin(refB, "probe"); err != nil {
		t.Fatalf("refB should still exist (unpinned, not deleted): %v", err)
	}
	al.releaseSessionPins(session)
	al.releaseSessionPins(session) // idempotent
}
