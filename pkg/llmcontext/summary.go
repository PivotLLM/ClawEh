// ClawEh
// License: MIT

package llmcontext

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

const summaryVersion = 1

// Summary is the structured session summary stored in meta.json and rendered
// into the system prompt. Stored as JSON; rendered as Markdown for injection.
type Summary struct {
	Version              int          `json:"version"`
	State                SummaryState `json:"state"`
	KeyMoments           []KeyMoment  `json:"key_moments,omitempty"`
	MessageIndex         []IndexEntry `json:"message_index,omitempty"`
	CoveredSeqStart      int          `json:"covered_seq_start"`
	CoveredSeqEnd        int          `json:"covered_seq_end"`
	CoveredSeqStartAt    time.Time    `json:"covered_seq_start_at,omitempty"`
	CoveredSeqEndAt      time.Time    `json:"covered_seq_end_at,omitempty"`
	GeneratedAt          time.Time    `json:"generated_at"`
	Model                string       `json:"model,omitempty"`
}

// SummaryState holds the four dynamic sections of the current state.
type SummaryState struct {
	Goals       string `json:"goals,omitempty"`
	Progress    string `json:"progress,omitempty"`
	Pending     string `json:"pending,omitempty"`
	Constraints string `json:"constraints,omitempty"`
}

// KeyMoment records a single high-importance event in the conversation.
type KeyMoment struct {
	Seq     int    `json:"seq"`
	Role    string `json:"role"`
	Summary string `json:"summary"`
	Exact   string `json:"exact,omitempty"`
}

// IndexEntry is one entry (or a collapsed range) in the retrievable message index.
type IndexEntry struct {
	SeqStart int    `json:"seq_start"`
	SeqEnd   int    `json:"seq_end"`
	Role     string `json:"role"`
	Label    string `json:"label"`
}

// Validate returns an error if the summary is malformed.
func (s *Summary) Validate() error {
	if s == nil {
		return fmt.Errorf("summary is nil")
	}
	if s.Version != summaryVersion {
		return fmt.Errorf("unsupported summary version: %d", s.Version)
	}
	if s.CoveredSeqStart < 0 || s.CoveredSeqEnd < 0 {
		return fmt.Errorf("negative seq range: %d..%d", s.CoveredSeqStart, s.CoveredSeqEnd)
	}
	if s.CoveredSeqEnd > 0 && s.CoveredSeqEnd < s.CoveredSeqStart {
		return fmt.Errorf("seq range end (%d) < start (%d)", s.CoveredSeqEnd, s.CoveredSeqStart)
	}
	return nil
}

// TruncateToFit removes the oldest key_moments and retrievable_history (MessageIndex)
// entries until the JSON-serialized summary fits within tokenLimit tokens (using the
// runes/4 heuristic). Returns true if any truncation was performed.
//
// "Oldest" is defined as lowest index position within each slice (index 0 first),
// which corresponds to the oldest events since both slices are appended in
// chronological order by the summarizer.
func (s *Summary) TruncateToFit(tokenLimit int) bool {
	if s == nil || tokenLimit <= 0 {
		return false
	}
	truncated := false
	for {
		data, err := json.Marshal(s)
		if err != nil {
			break
		}
		if len([]rune(string(data)))/4 <= tokenLimit {
			break
		}
		// Drop the oldest entry from whichever slice is larger to reduce size
		// while retaining as much recent history as possible.
		if len(s.KeyMoments) == 0 && len(s.MessageIndex) == 0 {
			break // nothing left to drop; caller will handle residual oversize
		}
		if len(s.KeyMoments) >= len(s.MessageIndex) && len(s.KeyMoments) > 0 {
			s.KeyMoments = s.KeyMoments[1:]
		} else if len(s.MessageIndex) > 0 {
			s.MessageIndex = s.MessageIndex[1:]
		} else {
			s.KeyMoments = s.KeyMoments[1:]
		}
		truncated = true
	}
	return truncated
}

// StripOutOfRangeSeqRefs removes KeyMoments and MessageIndex entries whose seq
// references fall outside [coveredSeqStart, archiveMaxSeq]. This prevents
// hallucinated or stale seq values emitted by the summarizer from appearing in
// the rendered summary.
//
// coveredSeqStart is the start of the range covered by this summary.
// archiveMaxSeq is the highest seq currently in the archive (0 means unknown:
// only the coveredSeqStart floor is enforced in that case).
func (s *Summary) StripOutOfRangeSeqRefs(coveredSeqStart, archiveMaxSeq int) {
	if s == nil {
		return
	}

	inRange := func(seq int) bool {
		if seq < coveredSeqStart {
			return false
		}
		if archiveMaxSeq > 0 && seq > archiveMaxSeq {
			return false
		}
		return true
	}

	// Filter KeyMoments.
	valid := s.KeyMoments[:0]
	for _, km := range s.KeyMoments {
		if inRange(km.Seq) {
			valid = append(valid, km)
		}
	}
	s.KeyMoments = valid

	// Filter MessageIndex (retrievable_history). An entry is valid if both
	// SeqStart and SeqEnd are within the allowed range.
	validIdx := s.MessageIndex[:0]
	for _, e := range s.MessageIndex {
		if inRange(e.SeqStart) && inRange(e.SeqEnd) {
			validIdx = append(validIdx, e)
		}
	}
	s.MessageIndex = validIdx
}

// Render produces the Markdown block injected at the CONTEXT_SUMMARY position.
func (s *Summary) Render(archiveMinSeq, archiveMaxSeq int) string {
	var sb strings.Builder

	if s.CoveredSeqStart > 0 {
		if !s.CoveredSeqStartAt.IsZero() && !s.CoveredSeqEndAt.IsZero() {
			startStr := s.CoveredSeqStartAt.UTC().Format("2006-01-02 15:04 UTC")
			endStr := s.CoveredSeqEndAt.UTC().Format("2006-01-02 15:04 UTC")
			fmt.Fprintf(&sb, "Context summary: messages #%d (%s) – #%d (%s). Full messages retrievable via get_session_messages.\n\n",
				s.CoveredSeqStart, startStr, s.CoveredSeqEnd, endStr)
		} else {
			fmt.Fprintf(&sb, "Context summary: messages #%d – #%d. Full messages retrievable via get_session_messages.\n\n",
				s.CoveredSeqStart, s.CoveredSeqEnd)
		}
	}

	sb.WriteString("## Current State\n")
	if s.State.Goals != "" {
		fmt.Fprintf(&sb, "**Goals:** %s\n", s.State.Goals)
	}
	if s.State.Progress != "" {
		fmt.Fprintf(&sb, "**Progress:** %s\n", s.State.Progress)
	}
	if s.State.Pending != "" {
		fmt.Fprintf(&sb, "**Pending:** %s\n", s.State.Pending)
	}
	if s.State.Constraints != "" {
		fmt.Fprintf(&sb, "**Constraints:** %s\n", s.State.Constraints)
	}

	if len(s.KeyMoments) > 0 {
		sb.WriteString("\n## Key Moments\n")
		for _, km := range s.KeyMoments {
			if km.Exact != "" {
				fmt.Fprintf(&sb, "- [#%d] %s: exact: %q\n", km.Seq, km.Role, km.Exact)
			} else {
				fmt.Fprintf(&sb, "- [#%d] %s: %s\n", km.Seq, km.Role, km.Summary)
			}
		}
	}

	// Only inject the message index for entries within the archive window.
	inWindow := make([]IndexEntry, 0, len(s.MessageIndex))
	for _, e := range s.MessageIndex {
		if e.SeqStart >= archiveMinSeq && e.SeqEnd <= archiveMaxSeq {
			inWindow = append(inWindow, e)
		}
	}
	if len(inWindow) > 0 {
		sb.WriteString("\n## Retrievable History (use mcp__claw__get_session_messages to fetch full content)\n")
		for _, e := range inWindow {
			if e.SeqStart == e.SeqEnd {
				fmt.Fprintf(&sb, "- [#%d] %s: %s\n", e.SeqStart, e.Role, e.Label)
			} else {
				fmt.Fprintf(&sb, "- [#%d–#%d] %s: %s\n", e.SeqStart, e.SeqEnd, e.Role, e.Label)
			}
		}
	}

	return strings.TrimRight(sb.String(), "\n")
}

// unmarshalSummary attempts to parse raw as a Summary. Returns nil if raw is
// empty or is not valid Summary JSON.
func unmarshalSummary(raw string) (*Summary, error) {
	if strings.TrimSpace(raw) == "" {
		return nil, nil
	}
	// Must look like JSON to avoid log spam on legacy prose summaries.
	trimmed := strings.TrimSpace(raw)
	if len(trimmed) == 0 || trimmed[0] != '{' {
		return nil, fmt.Errorf("not JSON")
	}
	var s Summary
	if err := json.Unmarshal([]byte(raw), &s); err != nil {
		return nil, err
	}
	if err := s.Validate(); err != nil {
		return nil, err
	}
	return &s, nil
}

// renderSummaryFromRaw returns the Markdown to inject into the system prompt.
// If raw is a valid Summary JSON it is rendered structurally. Otherwise it is
// returned as-is (legacy prose or empty string).
func renderSummaryFromRaw(raw string, archiveMinSeq, archiveMaxSeq int) string {
	s, err := unmarshalSummary(raw)
	if err != nil || s == nil {
		return raw // legacy prose or empty — pass through unchanged
	}
	return s.Render(archiveMinSeq, archiveMaxSeq)
}

// promptStandard is the standard summarization prompt. The LLM fills state,
// key_moments, and message_index; covered_seq_* are populated by the caller.
const promptStandard = `You are an AI assistant performing context summarization. Produce a JSON object matching this schema exactly:

{
  "version": 1,
  "state": {
    "goals": "<current active goals>",
    "progress": "<progress toward goals>",
    "pending": "<immediate next actions in flight>",
    "constraints": "<persistent rules and preferences>"
  },
  "key_moments": [
    {"seq": <int>, "role": "<role>", "summary": "<summary>", "exact": "<verbatim if needed>"}
  ],
  "message_index": [
    {"seq_start": <int>, "seq_end": <int>, "role": "<role>", "label": "<label>"}
  ]
}

Instructions:
- Goals/Progress: update dynamically — only active goals; retire completed/superseded ones.
- Pending: capture immediate next actions in flight at this moment. Answer "what was I about to do?"
- Constraints: preserve verbatim unless user explicitly changed them.
- Key Moments: curated high-importance events only. Use exact field for instructions, decisions, config values.
- Message Index: collapse consecutive identical entries to a range. Only include messages in the archive window (seq %d to %d).
- Respond with valid JSON only. No markdown fences, no prose.`

// promptAggressive is used when the context is approaching its size limit.
// It keeps the same schema as the standard prompt but instructs the LLM to
// produce more compact output: terse state fields, selective key moments, and
// aggressively collapsed message index ranges.
const promptAggressive = `You are an AI assistant performing urgent context summarization. Token budget is critical — be maximally concise without losing actionable state. Produce a JSON object matching this schema exactly:

{
  "version": 1,
  "state": {
    "goals": "<active goals, one sentence each>",
    "progress": "<current status per goal, one sentence each>",
    "pending": "<immediate next actions, one line each>",
    "constraints": "<essential rules only>"
  },
  "key_moments": [
    {"seq": <int>, "role": "<role>", "summary": "<15 words or fewer>", "exact": "<verbatim only if a literal value is required>"}
  ],
  "message_index": [
    {"seq_start": <int>, "seq_end": <int>, "role": "<role>", "label": "<topic label>"}
  ]
}

Rules:
- State: one concise sentence per item; omit any field that is empty.
- Key Moments: include only decisions and facts required to continue in-progress work; omit anything already captured in state or easily derivable from context. No hard cap — include what is needed, nothing more.
- Message Index: collapse aggressively into broad topic or project ranges (archive window: seq %d to %d); one entry per project/thread area rather than per message.
- Update the existing summary rather than replacing it — preserve active goals and constraints.
- Respond with valid JSON only. No markdown fences, no prose.`

// buildSummarizationPrompt returns the prompt for a summarization call.
// existing may be nil on the first cycle.
func buildSummarizationPrompt(existing *Summary, archiveMin, archiveMax int, aggressive bool) string {
	var base string
	if aggressive {
		base = fmt.Sprintf(promptAggressive, archiveMin, archiveMax)
	} else {
		base = fmt.Sprintf(promptStandard, archiveMin, archiveMax)
	}
	if existing == nil {
		return base
	}
	existingJSON, err := json.MarshalIndent(existing, "", "  ")
	if err != nil {
		return base
	}
	return base + "\n\nExisting summary for context (update with new messages):\n" + string(existingJSON)
}

// validateAndUnmarshalLLMResponse parses and validates an LLM-produced Summary JSON.
func validateAndUnmarshalLLMResponse(response string) (*Summary, error) {
	// Strip markdown fences if present (some providers add them despite instructions).
	cleaned := strings.TrimSpace(response)
	if strings.HasPrefix(cleaned, "```") {
		if idx := strings.Index(cleaned, "\n"); idx >= 0 {
			cleaned = cleaned[idx+1:]
		}
		cleaned = strings.TrimSuffix(strings.TrimSpace(cleaned), "```")
		cleaned = strings.TrimSpace(cleaned)
	}
	var s Summary
	if err := json.Unmarshal([]byte(cleaned), &s); err != nil {
		return nil, fmt.Errorf("unmarshal: %w", err)
	}
	if err := s.Validate(); err != nil {
		return nil, fmt.Errorf("validate: %w", err)
	}
	return &s, nil
}
