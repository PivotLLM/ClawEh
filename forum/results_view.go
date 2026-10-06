package forum

import (
	"path/filepath"
	"time"
)

// MaxResultInlineChars is how much of each result output forum_results
// returns inline, in Unicode characters. A longer output is cut at this many
// characters and followed by a note naming the file that holds the full
// text, which the launching agent's file tools can read.
const MaxResultInlineChars = 4000

// MaxResultInlineTotalChars is how much output text forum_results returns
// inline in all, in Unicode characters. Outputs are inlined in order until
// it is spent; each one after that is listed with its metadata and file
// only (inline_omitted). Together with MaxResultInlineChars it keeps the
// result small enough for a model's context.
const MaxResultInlineTotalChars = 16000

// ResultsView is what forum_results returns: the manifest with each result
// output's text inline (within MaxResultInlineChars and
// MaxResultInlineTotalChars) and every path relative to the launching
// agent's workspace.
type ResultsView struct {
	ForumID    string    `json:"forum_id"`
	Run        int       `json:"run"`
	Name       string    `json:"name"`
	Status     Status    `json:"status"`
	Reason     EndReason `json:"reason,omitempty"`
	LaunchedAt time.Time `json:"launched_at"`
	EndedAt    time.Time `json:"ended_at,omitzero"`
	Complete   bool      `json:"complete"`
	Calls      int       `json:"calls"`
	// Transcript is empty when the run has no transcript.md yet.
	Transcript string        `json:"transcript,omitempty"`
	Layers     []LayerOutput `json:"layers"`
	Omissions  []string      `json:"omissions,omitempty"`
	// OtherLayers lists the other layers' outputs when the result layers
	// have none (Result.OtherLayers), within the same inline limits.
	OtherLayers []LayerOutput `json:"other_layers,omitempty"`
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
// MaxResultInlineChars (Truncated), or left out once
// MaxResultInlineTotalChars is spent (InlineOmitted). An output whose file
// cannot be read is Unreadable, with no size or text; its File is shown
// only when the recorded path stays inside the forum.
type OutputView struct {
	Author        string `json:"author"`
	Layer         string `json:"layer"`
	Round         int    `json:"round"`
	Format        Format `json:"format"`
	Size          int    `json:"size"`
	File          string `json:"file,omitempty"`
	Text          string `json:"text"`
	Truncated     bool   `json:"truncated,omitempty"`
	InlineOmitted bool   `json:"inline_omitted,omitempty"`
	Unreadable    bool   `json:"unreadable,omitempty"`
}

// truncatedNote follows an output cut at its inline limit.
func truncatedNote(path string) string {
	return "(truncated; full text in " + path + ")"
}

// prefixReader returns the first keep characters of a forum-root-relative
// file and its length in characters (Store.ReadPrefix).
type prefixReader func(rel string, keep int) (string, int, error)

// newResultsView renders res for forum_results. prefix is the run's
// directory relative to the agent's workspace (forums/<id>/runs/<n>); the
// transcript is named only when hasTranscript.
func newResultsView(res *Result, prefix string, hasTranscript bool, read prefixReader) ResultsView {
	view := ResultsView{
		ForumID: res.ForumID, Run: res.Run, Name: res.Name, Status: res.Status, Reason: res.Reason,
		LaunchedAt: res.LaunchedAt, EndedAt: res.EndedAt, Complete: res.Complete, Calls: res.Calls,
		Omissions: res.Omissions,
	}
	if hasTranscript {
		view.Transcript = filepath.ToSlash(filepath.Join(prefix, res.Transcript))
	}
	budget := MaxResultInlineTotalChars
	view.Layers = renderLayers(res.Layers, prefix, read, &budget)
	if len(res.OtherLayers) > 0 {
		view.OtherLayers = renderLayers(res.OtherLayers, prefix, read, &budget)
	}
	return view
}

// renderLayers renders layers' outputs, spending budget (characters of
// inline text left) in order.
func renderLayers(layers []LayerResult, prefix string, read prefixReader, budget *int) []LayerOutput {
	out := make([]LayerOutput, 0, len(layers))
	for _, l := range layers {
		lo := LayerOutput{LayerID: l.LayerID, Ended: l.Ended, EndReason: l.EndReason, Outputs: make([]OutputView, 0, len(l.Outputs))}
		for _, o := range l.Outputs {
			ov := OutputView{Author: o.ParticipantID, Layer: o.LayerID, Round: o.Round, Format: o.Format}
			clean, err := rootRel(o.ContentFile)
			if err != nil {
				ov.Unreadable = true
				lo.Outputs = append(lo.Outputs, ov)
				continue
			}
			ov.File = filepath.ToSlash(filepath.Join(prefix, clean))
			limit := min(MaxResultInlineChars, *budget)
			// One character more than the limit tells a cut text apart.
			text, chars, err := read(clean, limit+1)
			if err != nil {
				ov.Unreadable = true
				lo.Outputs = append(lo.Outputs, ov)
				continue
			}
			ov.Size = chars
			switch {
			case limit == 0:
				ov.InlineOmitted = true
			case chars > limit:
				ov.Text = string([]rune(text)[:limit]) + "\n" + truncatedNote(ov.File)
				ov.Truncated = true
				*budget -= limit
			default:
				ov.Text = text
				*budget -= chars
			}
			lo.Outputs = append(lo.Outputs, ov)
		}
		out = append(out, lo)
	}
	return out
}
