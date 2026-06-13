// ClawEh
// License: MIT

package llmcontext

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/PivotLLM/ClawEh/pkg/cronmsg"
	"github.com/PivotLLM/ClawEh/pkg/logger"
	"github.com/PivotLLM/ClawEh/pkg/memory"
	"github.com/PivotLLM/ClawEh/pkg/providers"
)

// doCompress performs LLM-based compression of the conversation history.
// It summarizes the oldest messages into a structured Summary, retains a tail
// of recent messages, and persists the result. safetyNet=true enables fallback
// behavior (drop oldest groups) when all LLM clients fail.
func (m *Manager) doCompress(ctx context.Context, safetyNet bool) error {
	if m.cfg.notifyCallback != nil {
		m.cfg.notifyCallback("compression started")
	}
	defer func() {
		m.compressedAtCount = m.msgCount
		if m.cfg.notifyCallback != nil {
			m.cfg.notifyCallback("compression complete")
		}
	}()

	// Resolve clients: prefer explicit compressClients, fall back to primary llm.
	clients := m.compressClients
	if len(clients) == 0 {
		m.lastReport = &CompactionReport{SessionKey: m.sessionKey, Outcome: "nothing"}
		return nil // no client configured
	}

	// Separate system message from conversation.
	// Use GetHistoryWithSeqs to preserve seq numbers for the summarizer.
	storedHistory := m.store.GetHistoryWithSeqs(m.sessionKey)
	var sysMsg *memory.StoredMessage
	storedConversation := storedHistory
	if len(storedHistory) > 0 && storedHistory[0].Role == "system" {
		sys := storedHistory[0]
		sysMsg = &sys
		storedConversation = storedHistory[1:]
	}

	if len(storedConversation) == 0 {
		m.lastReport = &CompactionReport{SessionKey: m.sessionKey, Outcome: "nothing"}
		return nil
	}

	// Per-pass recorder: collects one entry per LLM invocation for the report,
	// and (when debug capture is on) appends verbatim request/response to
	// <workspace>/compact.jsonl.
	debugPath := ""
	if m.cfg.compactDebug && m.cfg.compressionProfileDir != "" {
		debugPath = filepath.Join(m.cfg.compressionProfileDir, "compact.jsonl")
	}
	rec := &compactionRecorder{
		sessionKey:     m.sessionKey,
		debugPath:      debugPath,
		failureDumpDir: m.cfg.compressFailureDumpDir,
	}
	beforeMsgs := len(storedConversation)
	beforeBytes := storedBytes(storedConversation)
	dateFrom, dateTo := storedDateRange(storedConversation)

	conversation := storedToPlain(storedConversation)
	tokensBeforeCompress := m.estTokens(conversation)
	targetPct := float64(m.cfg.normalPercent) * defaultCompressTargetFactor
	budget := m.cfg.contextWindow * m.cfg.retainTokenPercent / 100

	archiveMin, archiveMax := m.archiveWindow()
	existingSummary, _ := unmarshalSummary(m.store.GetSummary(m.sessionKey))

	// Load the agent's compression profile once per compression pass.
	compressionProfile := loadCompressionProfile(m.cfg.compressionProfileDir)

	// Compression loop: iteratively summarize the oldest portion of the
	// conversation until we reach the target percentage or exhaust iterations.
	// Item 13: enforce the max summary token budget. Computed once and passed to
	// callLLMChain so an oversized summary is rejected within the client chain
	// (advancing to the next model) rather than after it.
	summaryTokenLimit := m.cfg.maxSummaryTokens
	if summaryTokenLimit <= 0 && m.cfg.contextWindow > 0 {
		summaryTokenLimit = m.cfg.contextWindow * 20 / 100
	}

	currentStored := storedConversation
	currentConversation := conversation
	latestSummary := existingSummary
	llmSucceeded := false
	attemptedLLM := false // true once the LLM was actually invoked for a summary
	aggressive := false
	iterCount := 0
	tokensBeforeIteration := tokensBeforeCompress

	for {
		if iterCount >= defaultMaxCompressIterations {
			if !aggressive {
				aggressive = true
				iterCount = 0
				tokensBeforeIteration = m.estTokens(currentConversation)
				continue
			}
			break // exhausted both standard and aggressive prompt types
		}

		tail := selectTail(currentConversation, budget, m.cfg.retainMinMessages, m.estTokens)
		tailStart := len(currentConversation) - len(tail)
		toSummarize := currentStored[:tailStart]

		if len(toSummarize) == 0 {
			break // tail covers everything; nothing left to summarize
		}

		attemptedLLM = true
		newSummary, ok := callLLMChain(ctx, clients, latestSummary, toSummarize, archiveMin, archiveMax, aggressive, compressionProfile, summaryTokenLimit, m.sessionKey, rec)
		if !ok {
			iterCount++
			continue
		}
		if newSummary.Model == "" {
			newSummary.Model = m.cfg.compressModel.Primary
		}

		llmSucceeded = true
		latestSummary = newSummary
		currentStored = currentStored[tailStart:]
		currentConversation = tail

		tokensCurrent := m.estTokens(currentConversation)
		if m.cfg.contextWindow > 0 {
			pctCurrent := float64(tokensCurrent) * 100 / float64(m.cfg.contextWindow)
			if pctCurrent < targetPct {
				break // reached target
			}
		}

		gain := 1.0
		if tokensBeforeIteration > 0 {
			gain = 1 - float64(tokensCurrent)/float64(tokensBeforeIteration)
		}
		if gain < defaultMinLoopGain {
			if !aggressive {
				aggressive = true
				iterCount = 0
				tokensBeforeIteration = tokensCurrent
			} else {
				break // stalled even on aggressive prompt
			}
		} else {
			tokensBeforeIteration = tokensCurrent
		}

		iterCount++
	}

	var err error
	if !safetyNet {
		err = m.handleNormalPostLoop(ctx, sysMsg, currentStored, latestSummary, llmSucceeded, attemptedLLM, tokensBeforeCompress, targetPct)
	} else {
		err = m.handleSafetyNetPostLoop(ctx, sysMsg, storedConversation, currentStored, latestSummary, existingSummary, llmSucceeded)
	}
	m.lastReport = m.buildReport(rec, beforeMsgs, beforeBytes, dateFrom, dateTo, err)
	return err
}

// handleNormalPostLoop handles post-loop logic for the normal (non-safety-net) path.
func (m *Manager) handleNormalPostLoop(
	ctx context.Context,
	sysMsg *memory.StoredMessage,
	currentStored []memory.StoredMessage,
	latestSummary *Summary,
	llmSucceeded bool,
	attemptedLLM bool,
	tokensBeforeCompress int,
	targetPct float64,
) error {
	_ = ctx // reserved for future use
	currentConversation := storedToPlain(currentStored)
	if !llmSucceeded {
		if !attemptedLLM {
			// The retained tail already covers the whole conversation; there was
			// nothing to summarize. This is a benign no-op, not a failure.
			logger.InfoCF("llmcontext", "compression: nothing to compress (tail already covers conversation)", map[string]any{
				"session_key": m.sessionKey,
			})
			return ErrNothingToCompress
		}
		logger.WarnCF("llmcontext", "compression failed: every summarization model rejected the summary", map[string]any{
			"session_key": m.sessionKey,
		})
		return ErrCompressionFailed
	}

	if err := m.persistStoredResult(sysMsg, currentStored, latestSummary); err != nil {
		return err
	}

	tokensFinal := m.estTokens(currentConversation)
	overallGain := 0.0
	if tokensBeforeCompress > 0 {
		overallGain = 1 - float64(tokensFinal)/float64(tokensBeforeCompress)
	}
	m.lastCompressionGain = overallGain
	m.lastCompressedAt = time.Now()

	finalPct := 0.0
	if m.cfg.contextWindow > 0 {
		finalPct = float64(tokensFinal) * 100 / float64(m.cfg.contextWindow)
	}
	if overallGain < defaultMinCompressionGain && finalPct >= float64(m.cfg.normalPercent) {
		m.cooling = true
		m.coolingSinceCount = m.msgCount
	}

	return nil
}

// handleSafetyNetPostLoop handles post-loop logic for the safety-net path.
func (m *Manager) handleSafetyNetPostLoop(
	ctx context.Context,
	sysMsg *memory.StoredMessage,
	originalStored []memory.StoredMessage,
	currentStored []memory.StoredMessage,
	latestSummary *Summary,
	existingSummary *Summary,
	llmSucceeded bool,
) error {
	if llmSucceeded {
		if err := m.persistStoredResult(sysMsg, currentStored, latestSummary); err != nil {
			return err
		}
	} else if existingSummary != nil {
		logger.WarnCF("llmcontext", "safety-net compression: all LLM clients failed; using stale summary", map[string]any{
			"session_key":   m.sessionKey,
			"stale_summary": true,
		})
		currentStored = originalStored
	} else {
		logger.WarnCF("llmcontext", "safety-net compression: all LLM clients failed; no summary available", map[string]any{
			"session_key": m.sessionKey,
			"no_summary":  true,
		})
		currentStored = originalStored
	}

	currentConversation := storedToPlain(currentStored)
	tokensFinal := m.estTokens(currentConversation)
	finalPct := 0.0
	if m.cfg.contextWindow > 0 {
		finalPct = float64(tokensFinal) * 100 / float64(m.cfg.contextWindow)
	}

	if finalPct < float64(m.cfg.safetyPercent) {
		// Compression was sufficient; clear cooling and update stats.
		m.cooling = false
		m.lastCompressionGain = 0
		m.lastCompressedAt = time.Now()
		return nil
	}

	// Still at or above safety threshold — drop oldest groups, then apply large
	// message checks, then persist. Order: group-drop -> applyLargeMsgChecks ->
	// persistStoredResult -> compute finalPct.
	currentStored = m.dropOldestStoredGroups(ctx, currentStored)
	m.applyLargeMsgChecksStored(currentStored)
	if err := m.persistStoredResult(sysMsg, currentStored, latestSummary); err != nil {
		return err
	}

	// Recheck after drops.
	currentConversation = storedToPlain(currentStored)
	tokensFinal = m.estTokens(currentConversation)
	if m.cfg.contextWindow > 0 {
		finalPct = float64(tokensFinal) * 100 / float64(m.cfg.contextWindow)
	}
	if finalPct < float64(m.cfg.safetyPercent) {
		m.cooling = false
		m.lastCompressedAt = time.Now()
		return nil
	}

	return ErrCompressionPartial
}

// callLLMChain calls each client in order, returning the first Summary that
// passes validation. A client whose summary fails to parse, lacks cited material,
// or cannot be trimmed within summaryTokenLimit is skipped and the next client is
// tried — so a fully-configured summarization chain (global models + agent
// primary) actually falls through on a model that returns an unacceptable summary,
// not just on a hard transport error. Returns (nil, false) if every client fails.
//
// toSummarize is a []memory.StoredMessage so the prompt can include [#N] seq
// prefixes for each message. CoveredSeqStart and CoveredSeqEnd are set from the
// actual min/max seq of the slice, not from any LLM-emitted values.
func callLLMChain(
	ctx context.Context,
	clients []LLMClient,
	existing *Summary,
	toSummarize []memory.StoredMessage,
	archiveMin, archiveMax int64,
	aggressive bool,
	compressionProfile string,
	summaryTokenLimit int,
	sessionKey string,
	rec *compactionRecorder,
) (*Summary, bool) {
	if len(toSummarize) == 0 {
		return nil, false
	}

	// Compute the actual seq range from the slice (do not trust LLM output).
	coveredStart := toSummarize[0].Seq
	coveredEnd := toSummarize[len(toSummarize)-1].Seq
	for _, sm := range toSummarize {
		if sm.Seq < coveredStart {
			coveredStart = sm.Seq
		}
		if sm.Seq > coveredEnd {
			coveredEnd = sm.Seq
		}
	}

	prompt := buildSummarizationPrompt(existing, archiveMin, archiveMax, aggressive, compressionProfile)
	formatted := formatStoredMessagesForSummary(toSummarize)

	messages := []providers.Message{
		{Role: "system", Content: prompt},
		{Role: "user", Content: "Messages to summarize:\n\n" + formatted},
	}

	for _, client := range clients {
		model := clientModel(client)
		start := time.Now()
		response, err := client.Complete(ctx, messages)
		dur := time.Since(start)
		if err != nil {
			logger.WarnCF("llmcontext", "LLM compression call failed", map[string]any{
				"error": err.Error(),
			})
			rec.record(model, "error", shortErr(err), dur, messages, "")
			continue
		}

		summary, perr := validateAndUnmarshalLLMResponse(response.Content)
		if perr != nil {
			logger.WarnCF("llmcontext", "LLM compression response parse failed", map[string]any{
				"error": perr.Error(),
			})
			rec.record(model, "error", "invalid JSON response", dur, messages, response.Content)
			continue
		}

		// Set coverage from actual seq ranges. Do NOT use coverage values
		// emitted by the LLM itself.
		applySummaryCoverage(summary, existing, coveredStart, coveredEnd, toSummarize)
		summary.GeneratedAt = time.Now()
		summary.Profile = profileFingerprint(compressionProfile)

		// Strip seq references outside the valid range, then require the summary
		// to carry cited material. A model that returns an un-cited summary is
		// skipped so the chain advances to the next model.
		summary.StripOutOfRangeSeqRefs(archiveMin, archiveMax)
		if !summary.HasMaterial() || !summary.HasEvidence() {
			logger.WarnCF("llmcontext", "LLM compression summary lacked cited material", map[string]any{
				"session_key": sessionKey,
			})
			rec.record(model, "rejected", "missing citations", dur, messages, response.Content)
			continue
		}

		// Item 13: enforce the max summary token budget. Truncate, then discard
		// (advancing to the next client) if it still does not fit.
		if summaryTokenLimit > 0 {
			if summary.TruncateToFit(summaryTokenLimit) {
				logger.WarnCF("llmcontext", "summary truncated to fit token budget", map[string]any{
					"session_key": sessionKey,
					"limit":       summaryTokenLimit,
				})
			}
			if data, merr := json.Marshal(summary); merr == nil {
				if len([]rune(string(data)))/4 > summaryTokenLimit {
					logger.WarnCF("llmcontext", "summary still oversized after truncation — discarding", map[string]any{
						"session_key": sessionKey,
						"tokens":      len([]rune(string(data))) / 4,
						"limit":       summaryTokenLimit,
					})
					rec.record(model, "rejected", "summary too large", dur, messages, response.Content)
					continue
				}
			}
		}

		rec.record(model, "ok", "", dur, messages, response.Content)
		return summary, true
	}

	return nil, false
}

// profileFingerprint returns a short sha256 hex[:8] fingerprint of a compression
// profile's content, or "" when no profile is in effect. Stamped into the summary
// so a reader can tell which profile shaped it and detect when the profile changed.
func profileFingerprint(profile string) string {
	p := strings.TrimSpace(profile)
	if p == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(p))
	return hex.EncodeToString(sum[:])[:8]
}

// loadCompressionProfile reads COMPRESSION.md (or the legacy compression.md)
// from dir if it exists. HTML comments are stripped so the template's
// human-facing documentation never reaches the summarizer; only real
// role-specific guidance is appended to the prompt. Returns "" when neither file
// is present, unreadable, or comment-only.
func loadCompressionProfile(dir string) string {
	if dir == "" {
		return ""
	}
	// Prefer the uppercase name to match the other workspace files (AGENTS.md,
	// SOUL.md, MEMORY.md, …); fall back to the legacy lowercase name.
	data, err := os.ReadFile(filepath.Join(dir, "COMPRESSION.md"))
	if err != nil {
		data, err = os.ReadFile(filepath.Join(dir, "compression.md"))
		if err != nil {
			return ""
		}
	}
	return strings.TrimSpace(stripHTMLComments(string(data)))
}

// stripHTMLComments removes <!-- ... --> blocks (including multi-line and
// unterminated ones) from s.
func stripHTMLComments(s string) string {
	for {
		start := strings.Index(s, "<!--")
		if start < 0 {
			break
		}
		rest := s[start+len("<!--"):]
		end := strings.Index(rest, "-->")
		if end < 0 {
			s = s[:start] // unterminated comment: drop to end
			break
		}
		s = s[:start] + rest[end+len("-->"):]
	}
	return s
}

// repetitiveRunThreshold is the minimum number of consecutive near-identical
// messages required before they are collapsed into a count annotation.
const repetitiveRunThreshold = 3

// normalizeForComparison returns a whitespace-collapsed lowercase version of s
// used to detect near-identical messages in repetitive run detection.
func normalizeForComparison(s string) string {
	return strings.ToLower(strings.Join(strings.Fields(s), " "))
}

func storedToPlain(stored []memory.StoredMessage) []providers.Message {
	msgs := make([]providers.Message, len(stored))
	for i, sm := range stored {
		msgs[i] = sm.Message
	}
	return msgs
}

// cronNoOpReplyMaxLen bounds how long an assistant reply may be (after trim)
// to still count as a "routine" no-op reply eligible for cron run collapse.
// This avoids collapsing substantive replies that merely happen to repeat.
const cronNoOpReplyMaxLen = 200

// collapseRepetitiveRuns scans stored messages for two kinds of repetition and
// replaces each run with a single counted anchor so the LLM summarizer handles
// them correctly instead of silently ignoring them:
//
//  1. Cron no-op runs: maximal runs of [cron-marker user message with the SAME
//     key] → [assistant reply] pairs where every assistant reply in the run is
//     mutually identical (trimmed-equal) and short. Each cron fire embeds a
//     different timestamp, so a byte-identical check would miss them entirely;
//     the marker key (fingerprint, or payload for legacy fires) lets us group
//     them. Seq of the first message is preserved.
//  2. Byte-identical same-role runs: the original behavior, applied to anything
//     not consumed by cron collapse.
func collapseRepetitiveRuns(stored []memory.StoredMessage) []memory.StoredMessage {
	if len(stored) < repetitiveRunThreshold {
		return stored
	}
	result := make([]memory.StoredMessage, 0, len(stored))
	i := 0
	for i < len(stored) {
		if anchor, next, ok := collapseCronRun(stored, i); ok {
			result = append(result, anchor)
			i = next
			continue
		}

		j := i + 1
		norm := normalizeForComparison(stored[i].Content)
		for j < len(stored) &&
			stored[j].Role == stored[i].Role &&
			normalizeForComparison(stored[j].Content) == norm {
			j++
		}
		runLen := j - i
		if runLen >= repetitiveRunThreshold {
			// Emit a single representative entry with a count annotation.
			collapsed := stored[i]
			collapsed.Content = fmt.Sprintf(
				"[REPEATED %d TIMES — content identical to above]\n%s",
				runLen, stored[i].Content,
			)
			result = append(result, collapsed)
		} else {
			result = append(result, stored[i])
			j = i + 1
		}
		i = j
	}
	return result
}

// collapseCronRun attempts to detect a cron no-op run starting at index start.
// A qualifying run is a maximal sequence of consecutive [cron-marker user
// message with the SAME collapse key] → [assistant reply] pairs where all the
// assistant replies are mutually trimmed-equal and short (<= cronNoOpReplyMaxLen).
// It returns the synthetic counted anchor, the index immediately after the run,
// and true. If no qualifying run of >= repetitiveRunThreshold fires begins at
// start, ok is false.
func collapseCronRun(stored []memory.StoredMessage, start int) (memory.StoredMessage, int, bool) {
	fp, _, isCron := cronmsg.Parse(stored[start].Content)
	if stored[start].Role != "user" || !isCron {
		return memory.StoredMessage{}, start, false
	}

	key, _ := cronmsg.CollapseKey(stored[start].Content)
	var reply string
	haveReply := false
	count := 0
	i := start

	for i+1 < len(stored) {
		userMsg := stored[i]
		replyMsg := stored[i+1]

		if userMsg.Role != "user" {
			break
		}
		k, ok := cronmsg.CollapseKey(userMsg.Content)
		if !ok || k != key {
			break
		}
		if replyMsg.Role != "assistant" {
			break
		}
		// Action detection: a reply that issued tool calls means the LLM DID
		// something in response to this fire (restarted a service, sent a message,
		// wrote a file, ...) — it is not routine noise. Stop the run here so the
		// acting fire and the entire tool exchange that follows it (tool results,
		// follow-up assistant turns) are preserved verbatim, never folded into the
		// collapsed count. A reply with no tool calls is a complete [cron → reply]
		// turn; the loop's own cron-key check ends the run when the next message
		// isn't another fire of the same job.
		if len(replyMsg.ToolCalls) > 0 {
			break
		}
		r := strings.TrimSpace(replyMsg.Content)
		if len(r) > cronNoOpReplyMaxLen {
			break
		}
		if !haveReply {
			reply = r
			haveReply = true
		} else if r != reply {
			// A differing reply means something actually happened — it breaks
			// the run. The accumulated uniform prefix is collapsed; this pair is
			// preserved verbatim by the caller continuing from the run boundary.
			break
		}

		count++
		i += 2
	}

	if count < repetitiveRunThreshold {
		return memory.StoredMessage{}, start, false
	}

	// The run spans stored[start] (first cron message) through stored[i-1] (last
	// consumed assistant reply). The anchor carries the seq of the FIRST message
	// and notes the full [first-last] seq range in its text. Seqs are permanent
	// identities — they are never renumbered — so the collapsed-away messages
	// simply do not appear inline in the retained tail; they remain intact in the
	// archive and stay retrievable by seq via get_session_messages.
	firstSeq := stored[start].Seq
	lastSeq := stored[i-1].Seq

	anchor := stored[start] // carry the seq of the first message in the run.
	anchor.Role = "user"
	anchor.Content = cronRunAnchor(fp, count, firstSeq, lastSeq, reply)
	anchor.ToolCalls = nil
	anchor.ToolCallID = ""
	return anchor, i, true
}

// cronRunAnchor renders the counted anchor string for a collapsed cron no-op run.
// It states the count and the [firstSeq-lastSeq] range so a reader knows exactly
// which archived messages were elided and can retrieve them via get_session_messages.
func cronRunAnchor(fingerprint string, count int, firstSeq, lastSeq int64, reply string) string {
	shortReply := truncateRunes(reply, 60)
	if fingerprint != "" {
		return fmt.Sprintf(
			"[scheduled job %s fired ×%d (#%d-#%d); routine, replies identical: %q]",
			fingerprint, count, firstSeq, lastSeq, shortReply,
		)
	}
	return fmt.Sprintf(
		"[scheduled job fired ×%d (#%d-#%d); routine, replies identical: %q]",
		count, firstSeq, lastSeq, shortReply,
	)
}

// collapseRetainedCronRuns collapses runs of repeated cron no-op fires in the
// RETAINED live tail into a single counted anchor — the same transformation the
// summarizer input receives — so the kept context window does not carry many
// identical scheduled checks verbatim. Only cron no-op runs are collapsed; every
// other message is preserved unchanged. The anchor carries the seq of the first
// message in the run; the elided originals remain in the archive (retrievable via
// get_session_messages), so this elides them only from the live tail, never from
// the durable record.
func collapseRetainedCronRuns(stored []memory.StoredMessage) []memory.StoredMessage {
	if len(stored) < repetitiveRunThreshold {
		return stored
	}
	result := make([]memory.StoredMessage, 0, len(stored))
	i := 0
	for i < len(stored) {
		if anchor, next, ok := collapseCronRun(stored, i); ok {
			result = append(result, anchor)
			i = next
			continue
		}
		result = append(result, stored[i])
		i++
	}
	return result
}

// truncateRunes returns s clipped to at most max runes, appending an ellipsis
// when truncation occurs.
func truncateRunes(s string, max int) string {
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	return string(r[:max]) + "…"
}

func formatStoredMessagesForSummary(stored []memory.StoredMessage) string {
	stored = collapseRepetitiveRuns(stored)
	var sb strings.Builder
	for _, sm := range stored {
		fmt.Fprintf(&sb, "[#%d] [%s]\n", sm.Seq, sm.Role)
		if sm.Source != "" {
			fmt.Fprintf(&sb, "source: %s\n", sm.Source)
		}
		if sm.ToolCallID != "" {
			fmt.Fprintf(&sb, "tool_call_id: %s\n", sm.ToolCallID)
		}
		if strings.TrimSpace(sm.Content) != "" {
			fmt.Fprintf(&sb, "content:\n%s\n", sm.Content)
		} else {
			sb.WriteString("content: <empty>\n")
		}
		if len(sm.ToolCalls) > 0 {
			sb.WriteString("tool_calls:\n")
			for _, tc := range sm.ToolCalls {
				name := tc.Name
				args := ""
				if tc.Function != nil {
					if tc.Function.Name != "" {
						name = tc.Function.Name
					}
					args = tc.Function.Arguments
				}
				if args == "" && len(tc.Arguments) > 0 {
					if data, err := json.Marshal(tc.Arguments); err == nil {
						args = string(data)
					}
				}
				fmt.Fprintf(&sb, "- id: %s\n  name: %s\n", tc.ID, name)
				if args != "" {
					fmt.Fprintf(&sb, "  arguments: %s\n", args)
				}
			}
		}
		sb.WriteString("\n")
	}
	return sb.String()
}

func applySummaryCoverage(summary, existing *Summary, coveredStart, coveredEnd int64, summarized []memory.StoredMessage) {
	if summary == nil {
		return
	}
	summary.CoveredSeqStart = coveredStart
	summary.CoveredSeqEnd = coveredEnd
	if existing != nil {
		if existing.CoveredSeqStart > 0 && (summary.CoveredSeqStart == 0 || existing.CoveredSeqStart < summary.CoveredSeqStart) {
			summary.CoveredSeqStart = existing.CoveredSeqStart
		}
		if existing.CoveredSeqEnd > summary.CoveredSeqEnd {
			summary.CoveredSeqEnd = existing.CoveredSeqEnd
		}
	}
	ranges := make([]SeqRange, 0, len(summary.CoveredRanges)+len(summarized)+1)
	if existing != nil {
		if len(existing.CoveredRanges) > 0 {
			ranges = append(ranges, existing.CoveredRanges...)
		} else if existing.CoveredSeqStart > 0 && existing.CoveredSeqEnd >= existing.CoveredSeqStart {
			ranges = append(ranges, SeqRange{SeqStart: existing.CoveredSeqStart, SeqEnd: existing.CoveredSeqEnd})
		}
	}
	ranges = append(ranges, SeqRange{SeqStart: coveredStart, SeqEnd: coveredEnd})
	summary.CoveredRanges = mergeSeqRanges(ranges)
	summary.LastSummarizedSeq = coveredEnd
	summary.LastSummarizedRange = SeqRange{SeqStart: coveredStart, SeqEnd: coveredEnd}
	if len(summarized) > 0 {
		startAt := summarized[0].CreatedAt
		endAt := summarized[len(summarized)-1].CreatedAt
		if existing != nil && !existing.CoveredSeqStartAt.IsZero() && existing.CoveredSeqStart <= coveredStart {
			startAt = existing.CoveredSeqStartAt
		}
		summary.CoveredSeqStartAt = startAt
		summary.CoveredSeqEndAt = endAt
	}
}

func mergeSeqRanges(ranges []SeqRange) []SeqRange {
	out := make([]SeqRange, 0, len(ranges))
	for _, r := range ranges {
		if r.SeqStart <= 0 {
			continue
		}
		if r.SeqEnd == 0 {
			r.SeqEnd = r.SeqStart
		}
		if r.SeqEnd < r.SeqStart {
			continue
		}
		if len(out) == 0 {
			out = append(out, r)
			continue
		}
		last := &out[len(out)-1]
		if r.SeqStart <= last.SeqEnd+1 {
			if r.SeqEnd > last.SeqEnd {
				last.SeqEnd = r.SeqEnd
			}
			continue
		}
		out = append(out, r)
	}
	return out
}

// persistStoredResult writes the compressed history and summary to the store and saves.
// It returns ErrCompressionFailed if Save() fails.
// After a successful save it persists compaction state if the store supports it.
func (m *Manager) persistStoredResult(sysMsg *memory.StoredMessage, conv []memory.StoredMessage, summary *Summary) error {
	// Collapse repeated cron no-op runs in the retained tail before persisting,
	// so the live context window the LLM keeps seeing carries one counted anchor
	// instead of many identical scheduled checks. The elided fires remain in the
	// archive (retrievable by seq); only the live tail elides them. Idempotent:
	// an already-collapsed anchor is not a cron-marker message, so re-running it
	// leaves anchors untouched.
	conv = collapseRetainedCronRuns(conv)

	newStored := make([]memory.StoredMessage, 0, len(conv)+1)
	if sysMsg != nil {
		newStored = append(newStored, *sysMsg)
	}
	newStored = append(newStored, conv...)

	if sh, ok := m.store.(interface {
		SetHistoryWithSeqs(string, []memory.StoredMessage)
	}); ok {
		sh.SetHistoryWithSeqs(m.sessionKey, newStored)
	} else {
		m.store.SetHistory(m.sessionKey, storedToPlain(newStored))
	}

	summaryModel := ""
	summaryGeneratedAt := m.lastCompressedAt
	if summary != nil {
		if data, err := json.Marshal(summary); err == nil {
			raw := string(data)
			m.store.SetSummary(m.sessionKey, raw)
			// Persist the summary checkpoint into the per-session archive DB
			// (summaries table). Best-effort: log on error, never fail compaction.
			if a := m.getOrOpenArchive(); a != nil {
				srcRange := summary.LastSummarizedSeqRange()
				if _, appendErr := a.AppendSummary(memory.SummaryRecord{
					GeneratedAt:     summary.GeneratedAt,
					Model:           summary.Model,
					Profile:         summary.Profile,
					SourceSeqStart:  srcRange.SeqStart,
					SourceSeqEnd:    srcRange.SeqEnd,
					CoveredSeqStart: summary.CoveredSeqStart,
					CoveredSeqEnd:   summary.CoveredSeqEnd,
					Summary:         raw,
				}); appendErr != nil {
					logger.WarnCF("llmcontext", "compression: failed to append summary to archive", map[string]any{
						"session_key": m.sessionKey,
						"error":       appendErr.Error(),
					})
				}
				// Apply retention after each compaction so a long-running agent
				// prunes its archive incrementally as it goes. Best-effort.
				m.pruneArchive(a)
			}
		}
		summaryModel = summary.Model
		if !summary.GeneratedAt.IsZero() {
			summaryGeneratedAt = summary.GeneratedAt
		}
	}

	if err := m.store.Save(m.sessionKey); err != nil {
		logger.WarnCF("llmcontext", "compression: failed to save session", map[string]any{
			"session_key": m.sessionKey,
			"error":       err.Error(),
		})
		return fmt.Errorf("%w: save: %s", ErrCompressionFailed, err.Error())
	}

	// 9d. Persist compaction state if the store supports it.
	// Use m.msgCount for CompressedAtMeaningfulCount because the defer in doCompress
	// sets m.compressedAtCount = m.msgCount after this call returns.
	if cs, ok := m.store.(CompactionStateStore); ok {
		state := memory.CompactionState{
			MeaningfulCount:             m.msgCount,
			CompressedAtMeaningfulCount: m.msgCount,
			Cooling:                     m.cooling,
			CoolingSinceCount:           m.coolingSinceCount,
			SummaryGeneratedAt:          summaryGeneratedAt,
			SummaryModel:                summaryModel,
		}
		if setErr := cs.SetCompactionState(m.sessionKey, state); setErr != nil {
			logger.WarnCF("llmcontext", "compression: failed to persist compaction state", map[string]any{
				"session_key": m.sessionKey,
				"error":       setErr.Error(),
			})
		}
	}

	return nil
}

// dropOldestStoredGroups removes the oldest turn groups (seq-preserving) from
// conv until the estimated token count drops below safetyPercent or conv reaches
// retainMinMessages.
func (m *Manager) dropOldestStoredGroups(_ context.Context, conv []memory.StoredMessage) []memory.StoredMessage {
	for {
		if len(conv) <= m.cfg.retainMinMessages {
			break
		}
		plain := storedToPlain(conv)
		tokens := m.estTokens(plain)
		pct := 0.0
		if m.cfg.contextWindow > 0 {
			pct = float64(tokens) * 100 / float64(m.cfg.contextWindow)
		}
		if pct < float64(m.cfg.safetyPercent) {
			break
		}

		groupEnd := resolveGroup(plain, 0).end
		logger.WarnCF("llmcontext", "safety-net: dropping oldest turn group", map[string]any{
			"session_key": m.sessionKey,
			"group_end":   groupEnd,
			"tokens":      tokens,
			"pct":         pct,
		})
		conv = conv[groupEnd+1:]
	}
	return conv
}

// applyLargeMsgChecks truncates individual messages that exceed the per-message
// size threshold. The last message is never truncated if it is a user message,
// since it is the current trigger.
func (m *Manager) applyLargeMsgChecks(conv []providers.Message) {
	if m.cfg.contextWindow <= 0 {
		return
	}
	threshold := m.cfg.contextWindow * (m.cfg.safetyPercent - defaultLargeMsgOffset) / 100

	hardLimit := m.cfg.contextWindow * m.cfg.safetyPercent / 100

	for i := range conv {
		msgTokens := m.estTokens([]providers.Message{conv[i]})
		if msgTokens > threshold {
			// The very last message: warn only if user, truncate otherwise.
			if i == len(conv)-1 && conv[i].Role == "user" {
				if msgTokens > hardLimit {
					logger.WarnCF("llmcontext", "last user message exceeds hard context limit", map[string]any{
						"session_key": m.sessionKey,
						"msg_tokens":  msgTokens,
					})
				}
				continue
			}
			// Truncate: threshold * 5/2 runes is the char limit.
			maxRunes := threshold * 5 / 2
			runes := []rune(conv[i].Content)
			if len(runes) > maxRunes {
				conv[i].Content = string(runes[:maxRunes]) + " [**TRUNCATED DUE TO SIZE**]"
			}
		}
	}
}

func (m *Manager) applyLargeMsgChecksStored(conv []memory.StoredMessage) {
	if m.cfg.contextWindow <= 0 {
		return
	}
	plain := storedToPlain(conv)
	m.applyLargeMsgChecks(plain)
	for i := range conv {
		conv[i].Message = plain[i]
	}
}
