package forum

import (
	"path/filepath"
	"time"
	"unicode/utf8"
)

// MaxResultInlineChars is how much of each result output forum_results
// returns inline, in Unicode characters. A longer output is cut at exactly
// this many characters and followed by a note naming the file that holds
// the full text, which the launching agent's file tools can read. Keep it
// small enough that a few outputs fit comfortably in a model's context.
const MaxResultInlineChars = 4000

// ResultsView is what forum_results returns: the manifest with each result
// output's text inline (up to MaxResultInlineChars) and every path relative
// to the launching agent's workspace.
type ResultsView struct {
	ForumID    string        `json:"forum_id"`
	Name       string        `json:"name,omitempty"`
	Status     Status        `json:"status"`
	Reason     EndReason     `json:"reason,omitempty"`
	LaunchedAt time.Time     `json:"launched_at"`
	EndedAt    time.Time     `json:"ended_at,omitzero"`
	Complete   bool          `json:"complete"`
	Calls      int           `json:"calls"`
	Transcript string        `json:"transcript"`
	Layers     []LayerOutput `json:"layers"`
	Omissions  []string      `json:"omissions,omitempty"`
}

// LayerOutput is one result layer in a ResultsView.
type LayerOutput struct {
	LayerID   string       `json:"layer_id"`
	Ended     bool         `json:"ended"`
	EndReason EndReason    `json:"end_reason,omitempty"`
	Outputs   []OutputView `json:"outputs"`
}

// OutputView is one published result output: who wrote it, where, its size
// in characters, the file with the full text and the text itself, cut at
// MaxResultInlineChars.
type OutputView struct {
	Author    string `json:"author"`
	Layer     string `json:"layer"`
	Round     int    `json:"round"`
	Format    Format `json:"format"`
	Size      int    `json:"size"`
	File      string `json:"file"`
	Text      string `json:"text"`
	Truncated bool   `json:"truncated,omitempty"`
}

// truncatedNote follows an output cut at MaxResultInlineChars.
func truncatedNote(path string) string {
	return "(truncated; full text in " + path + ")"
}

// newResultsView renders res for forum_results. prefix is the forum
// directory relative to the agent's workspace (forums/<id>); read reads a
// file by its forum-root-relative path. An output that cannot be read is
// listed with its path and no text.
func newResultsView(res *Result, prefix string, read func(rel string) ([]byte, error)) ResultsView {
	view := ResultsView{
		ForumID: res.ForumID, Name: res.Name, Status: res.Status, Reason: res.Reason,
		LaunchedAt: res.LaunchedAt, EndedAt: res.EndedAt, Complete: res.Complete, Calls: res.Calls,
		Transcript: filepath.ToSlash(filepath.Join(prefix, res.Transcript)),
		Layers:     make([]LayerOutput, 0, len(res.Layers)),
		Omissions:  res.Omissions,
	}
	for _, l := range res.Layers {
		lo := LayerOutput{LayerID: l.LayerID, Ended: l.Ended, EndReason: l.EndReason, Outputs: make([]OutputView, 0, len(l.Outputs))}
		for _, o := range l.Outputs {
			ov := OutputView{
				Author: o.ParticipantID, Layer: o.LayerID, Round: o.Round, Format: o.Format,
				File: filepath.ToSlash(filepath.Join(prefix, o.ContentFile)),
			}
			if data, err := read(o.ContentFile); err == nil {
				ov.Size = utf8.RuneCount(data)
				ov.Text, ov.Truncated = inlineText(string(data), ov.File)
			}
			lo.Outputs = append(lo.Outputs, ov)
		}
		view.Layers = append(view.Layers, lo)
	}
	return view
}

// inlineText returns text whole when it has at most MaxResultInlineChars
// characters, else its first MaxResultInlineChars characters followed by
// truncatedNote(path).
func inlineText(text, path string) (string, bool) {
	if utf8.RuneCountInString(text) <= MaxResultInlineChars {
		return text, false
	}
	cut, n := 0, 0
	for i := range text {
		if n == MaxResultInlineChars {
			cut = i
			break
		}
		n++
	}
	return text[:cut] + "\n" + truncatedNote(path), true
}
