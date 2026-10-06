package forum

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"
)

// fakePrefix serves files from a map the way Store.ReadPrefix does.
func fakePrefix(files map[string]string) prefixReader {
	return func(rel string, keep int) (string, int, error) {
		s, ok := files[rel]
		if !ok {
			return "", 0, errors.New("missing")
		}
		r := []rune(s)
		return string(r[:min(keep, len(r))]), len(r), nil
	}
}

func outputAt(author, file string, round int) OutputRecord {
	return OutputRecord{LayerID: "report", Round: round, ParticipantID: author, Format: FormatText, ContentFile: file}
}

func resultWith(status Status, outs ...OutputRecord) *Result {
	return &Result{
		ForumID: "f1", Status: status, Complete: status == StatusCompleted, Transcript: fileTranscript,
		Layers: []LayerResult{{LayerID: "report", Ended: status == StatusCompleted, Outputs: outs}},
	}
}

// A short output is returned whole; a long one is cut at exactly
// MaxResultInlineChars characters and points at its file; the transcript
// path is relative to the agent's workspace.
func TestResultsView_InlineTextAndCap(t *testing.T) {
	long := strings.Repeat("é", MaxResultInlineChars+10)
	res := resultWith(StatusCompleted, outputAt("alice", "layers/report/a.txt", 1), outputAt("bob", "layers/report/b.txt", 2))
	view := newResultsView(res, "forums/f1", fakePrefix(map[string]string{
		"layers/report/a.txt": "Short answer.",
		"layers/report/b.txt": long,
	}))

	if view.Transcript != "forums/f1/transcript.md" {
		t.Errorf("transcript = %q", view.Transcript)
	}
	short, cut := view.Layers[0].Outputs[0], view.Layers[0].Outputs[1]
	if short.Author != "alice" || short.Layer != "report" || short.Round != 1 || short.Size != len("Short answer.") ||
		short.Text != "Short answer." || short.Truncated || short.File != "forums/f1/layers/report/a.txt" {
		t.Errorf("short output = %+v", short)
	}
	if cut.Size != MaxResultInlineChars+10 || !cut.Truncated {
		t.Fatalf("long output = size %d truncated %v", cut.Size, cut.Truncated)
	}
	note := "\n(truncated; full text in forums/f1/layers/report/b.txt)"
	if !strings.HasSuffix(cut.Text, note) {
		t.Fatalf("long output does not end with the note")
	}
	if body := strings.TrimSuffix(cut.Text, note); body != strings.Repeat("é", MaxResultInlineChars) {
		t.Errorf("cut body has %d characters, want %d", utf8.RuneCountInString(body), MaxResultInlineChars)
	}
}

// Exactly MaxResultInlineChars characters is not cut.
func TestResultsView_AtTheCap(t *testing.T) {
	text := strings.Repeat("x", MaxResultInlineChars)
	view := newResultsView(resultWith(StatusCompleted, outputAt("alice", "a.txt", 1)), "forums/f1", fakePrefix(map[string]string{"a.txt": text}))
	if ov := view.Layers[0].Outputs[0]; ov.Truncated || ov.Text != text {
		t.Fatalf("text at the cap was cut: truncated=%v", ov.Truncated)
	}
}

// Outputs are inlined in order until MaxResultInlineTotalChars is spent;
// the rest keep their metadata and file but no text.
func TestResultsView_TotalBudget(t *testing.T) {
	files := map[string]string{}
	n := MaxResultInlineTotalChars/MaxResultInlineChars + 2
	outs := make([]OutputRecord, 0, n)
	for i := range n {
		name := "o" + string(rune('a'+i)) + ".txt"
		files[name] = strings.Repeat("y", MaxResultInlineChars)
		outs = append(outs, outputAt("alice", name, i+1))
	}
	view := newResultsView(resultWith(StatusCompleted, outs...), "forums/f1", fakePrefix(files))
	total := 0
	for i, ov := range view.Layers[0].Outputs {
		total += utf8.RuneCountInString(ov.Text)
		inlined := i < MaxResultInlineTotalChars/MaxResultInlineChars
		if inlined != (ov.Text != "") || inlined == ov.InlineOmitted {
			t.Errorf("output %d: text %d chars, inline_omitted %v", i, len(ov.Text), ov.InlineOmitted)
		}
		if ov.Size != MaxResultInlineChars || ov.File == "" {
			t.Errorf("output %d metadata = %+v", i, ov)
		}
	}
	if total != MaxResultInlineTotalChars {
		t.Errorf("inlined %d characters, want %d", total, MaxResultInlineTotalChars)
	}
}

// A running forum's partial manifest renders its published outputs the
// same way. An unreadable output is flagged, and a recorded path leaving
// the forum is not shown.
func TestResultsView_PartialAndUnreadable(t *testing.T) {
	res := resultWith(StatusRunning,
		outputAt("alice", "a.txt", 1), outputAt("bob", "missing.txt", 1), outputAt("bob", "../../../etc/passwd", 2))
	view := newResultsView(res, "forums/f1", fakePrefix(map[string]string{"a.txt": "So far."}))
	if view.Complete || view.Status != StatusRunning || view.Layers[0].Ended {
		t.Errorf("partial view = %+v", view)
	}
	outs := view.Layers[0].Outputs
	if outs[0].Text != "So far." || outs[0].Size != 7 || outs[0].Unreadable {
		t.Errorf("published output = %+v", outs[0])
	}
	if !outs[1].Unreadable || outs[1].File != "forums/f1/missing.txt" {
		t.Errorf("unreadable output = %+v", outs[1])
	}
	if !outs[2].Unreadable || outs[2].File != "" {
		t.Errorf("escaping output = %+v", outs[2])
	}
}

// Store.ReadPrefix keeps only the requested prefix but counts every
// character, and refuses a path outside the forum.
func TestStoreReadPrefix(t *testing.T) {
	base := t.TempDir()
	store, err := CreateStore(base, "11111111-1111-4111-8111-111111111111")
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(store.Root(), "out.txt"), []byte("héllo wörld"), 0o600); err != nil {
		t.Fatal(err)
	}
	got, chars, err := store.ReadPrefix("out.txt", 3)
	if err != nil || got != "hél" || chars != 11 {
		t.Fatalf("ReadPrefix = %q, %d, %v", got, chars, err)
	}
	if _, _, err := store.ReadPrefix("../escape.txt", 3); err == nil {
		t.Fatal("a path outside the forum must be refused")
	}
}
