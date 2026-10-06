package forum

import (
	"errors"
	"strings"
	"testing"
	"unicode/utf8"
)

func viewFixture(status Status, files map[string]string) (*Result, func(string) ([]byte, error)) {
	res := &Result{
		ForumID: "f1", Status: status, Complete: status == StatusCompleted, Transcript: fileTranscript,
		Layers: []LayerResult{{LayerID: "report", Ended: status == StatusCompleted, Outputs: []OutputRecord{
			{LayerID: "report", Round: 1, ParticipantID: "alice", Format: FormatText, ContentFile: "layers/report/calls/a/1/output.txt"},
			{LayerID: "report", Round: 2, ParticipantID: "bob", Format: FormatText, ContentFile: "layers/report/calls/b/1/output.txt"},
		}}},
	}
	read := func(rel string) ([]byte, error) {
		if s, ok := files[rel]; ok {
			return []byte(s), nil
		}
		return nil, errors.New("missing")
	}
	return res, read
}

// A short output is returned whole; a long one is cut at exactly
// MaxResultInlineChars characters and points at its file; the transcript
// path is relative to the agent's workspace.
func TestResultsView_InlineTextAndCap(t *testing.T) {
	long := strings.Repeat("é", MaxResultInlineChars+10)
	res, read := viewFixture(StatusCompleted, map[string]string{
		"layers/report/calls/a/1/output.txt": "Short answer.",
		"layers/report/calls/b/1/output.txt": long,
	})
	view := newResultsView(res, "forums/f1", read)

	if view.Transcript != "forums/f1/transcript.md" {
		t.Errorf("transcript = %q", view.Transcript)
	}
	outs := view.Layers[0].Outputs
	short := outs[0]
	if short.Author != "alice" || short.Layer != "report" || short.Round != 1 || short.Size != len("Short answer.") ||
		short.Text != "Short answer." || short.Truncated || short.File != "forums/f1/layers/report/calls/a/1/output.txt" {
		t.Errorf("short output = %+v", short)
	}
	cut := outs[1]
	if cut.Size != MaxResultInlineChars+10 || !cut.Truncated {
		t.Fatalf("long output = size %d truncated %v", cut.Size, cut.Truncated)
	}
	note := "\n(truncated; full text in forums/f1/layers/report/calls/b/1/output.txt)"
	if !strings.HasSuffix(cut.Text, note) {
		t.Fatalf("long output text does not end with the note: %q", cut.Text[len(cut.Text)-120:])
	}
	if body := strings.TrimSuffix(cut.Text, note); utf8.RuneCountInString(body) != MaxResultInlineChars || body != strings.Repeat("é", MaxResultInlineChars) {
		t.Errorf("cut body has %d characters, want %d", utf8.RuneCountInString(body), MaxResultInlineChars)
	}
}

// Exactly MaxResultInlineChars characters is not truncated.
func TestResultsView_AtTheCap(t *testing.T) {
	text := strings.Repeat("x", MaxResultInlineChars)
	if got, cut := inlineText(text, "p"); cut || got != text {
		t.Fatalf("text at the cap was cut")
	}
}

// A running forum's partial manifest renders its published outputs the
// same way, and an unreadable output keeps its path without text.
func TestResultsView_PartialManifest(t *testing.T) {
	res, read := viewFixture(StatusRunning, map[string]string{"layers/report/calls/a/1/output.txt": "So far."})
	view := newResultsView(res, "forums/f1", read)
	if view.Complete || view.Status != StatusRunning || view.Layers[0].Ended {
		t.Errorf("partial view = %+v", view)
	}
	outs := view.Layers[0].Outputs
	if outs[0].Text != "So far." || outs[0].Size != 7 {
		t.Errorf("published output = %+v", outs[0])
	}
	if outs[1].Text != "" || outs[1].File != "forums/f1/layers/report/calls/b/1/output.txt" {
		t.Errorf("unreadable output = %+v", outs[1])
	}
}
