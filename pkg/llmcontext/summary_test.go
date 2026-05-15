// ClawEh
// License: MIT

package llmcontext

import (
	"strings"
	"testing"
	"time"
)

func validSummary() *Summary {
	return &Summary{
		Version: 1,
		State: SummaryState{
			Goals:       "finish feature X",
			Progress:    "80% done",
			Pending:     "write tests",
			Constraints: "no breaking changes",
		},
		CoveredSeqStart: 0,
		CoveredSeqEnd:   10,
		GeneratedAt:     time.Now(),
	}
}

// TestSummary_Validate_Valid — a well-formed Summary passes validation.
func TestSummary_Validate_Valid(t *testing.T) {
	s := validSummary()
	if err := s.Validate(); err != nil {
		t.Fatalf("expected nil error, got: %v", err)
	}
}

// TestSummary_Validate_WrongVersion — version ≠ 1 returns error.
func TestSummary_Validate_WrongVersion(t *testing.T) {
	s := validSummary()
	s.Version = 99
	if err := s.Validate(); err == nil {
		t.Fatal("expected error for wrong version, got nil")
	}
}

// TestSummary_Validate_NegativeSeq — negative seq returns error.
func TestSummary_Validate_NegativeSeq(t *testing.T) {
	tests := []struct {
		name  string
		start int
		end   int
	}{
		{"negative start", -1, 10},
		{"negative end", 0, -1},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s := validSummary()
			s.CoveredSeqStart = tc.start
			s.CoveredSeqEnd = tc.end
			if err := s.Validate(); err == nil {
				t.Fatalf("expected error for negative seq, got nil")
			}
		})
	}
}

// TestSummary_Render_AllSections — Render includes state, key moments, in-window index entries.
func TestSummary_Render_AllSections(t *testing.T) {
	s := validSummary()
	s.KeyMoments = []KeyMoment{
		{Seq: 3, Role: "user", Summary: "defined acceptance criteria"},
		{Seq: 5, Role: "assistant", Exact: "use mutex for shared state"},
	}
	s.MessageIndex = []IndexEntry{
		{SeqStart: 1, SeqEnd: 2, Role: "user", Label: "initial setup discussion"},
		{SeqStart: 4, SeqEnd: 4, Role: "assistant", Label: "proposed design"},
	}

	out := s.Render(0, 10)

	if !strings.Contains(out, "## Current State") {
		t.Error("missing Current State section")
	}
	if !strings.Contains(out, "finish feature X") {
		t.Error("missing Goals content")
	}
	if !strings.Contains(out, "## Key Moments") {
		t.Error("missing Key Moments section")
	}
	if !strings.Contains(out, "defined acceptance criteria") {
		t.Error("missing key moment summary")
	}
	if !strings.Contains(out, `"use mutex for shared state"`) {
		t.Error("missing key moment exact quote")
	}
	if !strings.Contains(out, "## Retrievable History") {
		t.Error("missing Retrievable History section")
	}
	if !strings.Contains(out, "initial setup discussion") {
		t.Error("missing index entry label")
	}
}

// TestSummary_Render_OutOfWindowIndexSkipped — index entries outside archive window are excluded.
func TestSummary_Render_OutOfWindowIndexSkipped(t *testing.T) {
	s := validSummary()
	s.MessageIndex = []IndexEntry{
		{SeqStart: 5, SeqEnd: 7, Role: "user", Label: "in window"},
		{SeqStart: 20, SeqEnd: 25, Role: "user", Label: "out of window"},
	}

	out := s.Render(0, 10)

	if !strings.Contains(out, "in window") {
		t.Error("expected in-window entry to be included")
	}
	if strings.Contains(out, "out of window") {
		t.Error("expected out-of-window entry to be excluded")
	}
}

// TestSummary_Render_NoIndex — no index section rendered when all entries out of window.
func TestSummary_Render_NoIndex(t *testing.T) {
	s := validSummary()
	s.MessageIndex = []IndexEntry{
		{SeqStart: 50, SeqEnd: 60, Role: "user", Label: "way out of window"},
	}

	out := s.Render(0, 10)

	if strings.Contains(out, "## Retrievable History") {
		t.Error("expected no Retrievable History section when all entries are out of window")
	}
}

// TestRenderSummaryFromRaw_Legacy — non-JSON raw string passes through unchanged.
func TestRenderSummaryFromRaw_Legacy(t *testing.T) {
	raw := "The user is working on feature X. Next step is to write tests."
	out := renderSummaryFromRaw(raw, 0, 0)
	if out != raw {
		t.Errorf("expected pass-through, got: %q", out)
	}
}

// TestRenderSummaryFromRaw_EmptyString — empty string passes through unchanged.
func TestRenderSummaryFromRaw_EmptyString(t *testing.T) {
	out := renderSummaryFromRaw("", 0, 0)
	if out != "" {
		t.Errorf("expected empty string, got: %q", out)
	}
}

// TestRenderSummaryFromRaw_ValidJSON — valid JSON summary renders as Markdown.
func TestRenderSummaryFromRaw_ValidJSON(t *testing.T) {
	raw := `{"version":1,"state":{"goals":"ship v2"},"covered_seq_start":0,"covered_seq_end":5,"generated_at":"2026-01-01T00:00:00Z"}`
	out := renderSummaryFromRaw(raw, 0, 0)

	if !strings.Contains(out, "## Current State") {
		t.Errorf("expected Markdown output, got: %q", out)
	}
	if !strings.Contains(out, "ship v2") {
		t.Errorf("expected goals content, got: %q", out)
	}
}

// TestValidateAndUnmarshalLLMResponse_CleanJSON — parses clean JSON correctly.
func TestValidateAndUnmarshalLLMResponse_CleanJSON(t *testing.T) {
	raw := `{"version":1,"state":{"goals":"build feature"},"covered_seq_start":0,"covered_seq_end":0,"generated_at":"2026-01-01T00:00:00Z"}`
	s, err := validateAndUnmarshalLLMResponse(raw)
	if err != nil {
		t.Fatalf("expected nil error, got: %v", err)
	}
	if s.State.Goals != "build feature" {
		t.Errorf("expected goals 'build feature', got: %q", s.State.Goals)
	}
}

// TestValidateAndUnmarshalLLMResponse_MarkdownFenced — strips markdown fences before parsing.
func TestValidateAndUnmarshalLLMResponse_MarkdownFenced(t *testing.T) {
	raw := "```json\n{\"version\":1,\"state\":{\"goals\":\"build feature\"},\"covered_seq_start\":0,\"covered_seq_end\":0,\"generated_at\":\"2026-01-01T00:00:00Z\"}\n```"
	s, err := validateAndUnmarshalLLMResponse(raw)
	if err != nil {
		t.Fatalf("expected nil error, got: %v", err)
	}
	if s.State.Goals != "build feature" {
		t.Errorf("expected goals 'build feature', got: %q", s.State.Goals)
	}
}

// TestBuildSummarizationPrompt_ContainsArchiveRange — prompt includes archive min/max.
func TestBuildSummarizationPrompt_ContainsArchiveRange(t *testing.T) {
	prompt := buildSummarizationPrompt(nil, 10, 50, false)
	if !strings.Contains(prompt, "10") {
		t.Error("expected archive min (10) in prompt")
	}
	if !strings.Contains(prompt, "50") {
		t.Error("expected archive max (50) in prompt")
	}
}

// TestBuildSummarizationPrompt_Aggressive — aggressive prompt keeps the same
// schema but instructs terse output and collapsed message index ranges.
func TestBuildSummarizationPrompt_Aggressive(t *testing.T) {
	aggressive := buildSummarizationPrompt(nil, 10, 100, true)
	if !strings.Contains(aggressive, `"message_index"`) {
		t.Error("aggressive prompt must include message_index in schema")
	}
	if !strings.Contains(aggressive, "10") || !strings.Contains(aggressive, "100") {
		t.Error("aggressive prompt must include archive window seq range")
	}
	if !strings.Contains(aggressive, "collapse") {
		t.Error("aggressive prompt must instruct collapsing message index into ranges")
	}
	// Existing summary should be appended when provided.
	existing := &Summary{Version: 1, State: SummaryState{Goals: "test goal"}}
	withExisting := buildSummarizationPrompt(existing, 10, 100, true)
	if !strings.Contains(withExisting, "test goal") {
		t.Error("aggressive prompt must include existing summary when provided")
	}
}
