// ClawEh
// License: MIT

package llmcontext

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

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
		return nil
	}

	conversation := storedToPlain(storedConversation)
	tokensBeforeCompress := estimateTokens(conversation)
	targetPct := float64(m.cfg.normalPercent) * defaultCompressTargetFactor
	budget := m.cfg.contextWindow * m.cfg.retainTokenPercent / 100

	archiveMin, archiveMax := m.archiveWindow()
	existingSummary, _ := unmarshalSummary(m.store.GetSummary(m.sessionKey))

	// Compression loop: iteratively summarize the oldest portion of the
	// conversation until we reach the target percentage or exhaust iterations.
	currentStored := storedConversation
	currentConversation := conversation
	latestSummary := existingSummary
	llmSucceeded := false
	aggressive := false
	iterCount := 0
	tokensBeforeIteration := tokensBeforeCompress

	for {
		if iterCount >= defaultMaxCompressIterations {
			if !aggressive {
				aggressive = true
				iterCount = 0
				tokensBeforeIteration = estimateTokens(currentConversation)
				continue
			}
			break // exhausted both standard and aggressive prompt types
		}

		tail := selectTail(currentConversation, budget, m.cfg.retainMinMessages)
		tailStart := len(currentConversation) - len(tail)
		toSummarize := currentStored[:tailStart]

		if len(toSummarize) == 0 {
			break // tail covers everything; nothing left to summarize
		}

		newSummary, ok := callLLMChain(ctx, clients, latestSummary, toSummarize, archiveMin, archiveMax, aggressive)
		if !ok {
			iterCount++
			continue
		}
		if newSummary.Model == "" {
			newSummary.Model = m.cfg.compressModel.Primary
		}

		// Strip any seq references outside the valid range before accepting the summary.
		newSummary.StripOutOfRangeSeqRefs(archiveMin, archiveMax)
		if !newSummary.HasMaterial() || !newSummary.HasEvidence() {
			logger.WarnCF("llmcontext", "LLM compression summary lacked cited material", map[string]any{
				"session_key": m.sessionKey,
			})
			iterCount++
			continue
		}

		// Item 13: enforce the max summary token budget.
		summaryTokenLimit := m.cfg.maxSummaryTokens
		if summaryTokenLimit <= 0 && m.cfg.contextWindow > 0 {
			summaryTokenLimit = m.cfg.contextWindow * 20 / 100
		}
		if summaryTokenLimit > 0 {
			if newSummary.TruncateToFit(summaryTokenLimit) {
				logger.WarnCF("llmcontext", "summary truncated to fit token budget", map[string]any{
					"session_key": m.sessionKey,
					"limit":       summaryTokenLimit,
				})
			}
			// After truncation, verify the summary still fits.
			if data, merr := json.Marshal(newSummary); merr == nil {
				if len([]rune(string(data)))/4 > summaryTokenLimit {
					logger.WarnCF("llmcontext", "summary still oversized after truncation — discarding", map[string]any{
						"session_key": m.sessionKey,
						"tokens":      len([]rune(string(data))) / 4,
						"limit":       summaryTokenLimit,
					})
					iterCount++
					continue
				}
			}
		}

		llmSucceeded = true
		latestSummary = newSummary
		currentStored = currentStored[tailStart:]
		currentConversation = tail

		tokensCurrent := estimateTokens(currentConversation)
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

	if !safetyNet {
		return m.handleNormalPostLoop(ctx, sysMsg, currentStored, latestSummary, llmSucceeded, tokensBeforeCompress, targetPct)
	}
	return m.handleSafetyNetPostLoop(ctx, sysMsg, storedConversation, currentStored, latestSummary, existingSummary, llmSucceeded)
}

// handleNormalPostLoop handles post-loop logic for the normal (non-safety-net) path.
func (m *Manager) handleNormalPostLoop(
	ctx context.Context,
	sysMsg *memory.StoredMessage,
	currentStored []memory.StoredMessage,
	latestSummary *Summary,
	llmSucceeded bool,
	tokensBeforeCompress int,
	targetPct float64,
) error {
	_ = ctx // reserved for future use
	currentConversation := storedToPlain(currentStored)
	if !llmSucceeded {
		logger.WarnCF("llmcontext", "compression failed: no LLM client succeeded", map[string]any{
			"session_key": m.sessionKey,
		})
		return ErrCompressionFailed
	}

	if err := m.persistStoredResult(sysMsg, currentStored, latestSummary); err != nil {
		return err
	}

	tokensFinal := estimateTokens(currentConversation)
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
	tokensFinal := estimateTokens(currentConversation)
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
	tokensFinal = estimateTokens(currentConversation)
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

// callLLMChain calls each client in order, returning the first successful Summary.
// It returns (nil, false) if all clients fail.
//
// toSummarize is a []memory.StoredMessage so the prompt can include [#N] seq
// prefixes for each message. CoveredSeqStart and CoveredSeqEnd are set from the
// actual min/max seq of the slice, not from any LLM-emitted values.
func callLLMChain(
	ctx context.Context,
	clients []LLMClient,
	existing *Summary,
	toSummarize []memory.StoredMessage,
	archiveMin, archiveMax int,
	aggressive bool,
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

	prompt := buildSummarizationPrompt(existing, archiveMin, archiveMax, aggressive)
	formatted := formatStoredMessagesForSummary(toSummarize)

	messages := []providers.Message{
		{Role: "system", Content: prompt},
		{Role: "user", Content: "Messages to summarize:\n\n" + formatted},
	}

	for _, client := range clients {
		response, err := client.Complete(ctx, messages)
		if err != nil {
			logger.WarnCF("llmcontext", "LLM compression call failed", map[string]any{
				"error": err.Error(),
			})
			continue
		}

		summary, err := validateAndUnmarshalLLMResponse(response.Content)
		if err != nil {
			logger.WarnCF("llmcontext", "LLM compression response parse failed", map[string]any{
				"error": err.Error(),
			})
			continue
		}

		// Set coverage from actual seq ranges. Do NOT use coverage values
		// emitted by the LLM itself.
		applySummaryCoverage(summary, existing, coveredStart, coveredEnd, toSummarize)
		summary.GeneratedAt = time.Now()
		return summary, true
	}

	return nil, false
}

func storedToPlain(stored []memory.StoredMessage) []providers.Message {
	msgs := make([]providers.Message, len(stored))
	for i, sm := range stored {
		msgs[i] = sm.Message
	}
	return msgs
}

func formatStoredMessagesForSummary(stored []memory.StoredMessage) string {
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

func applySummaryCoverage(summary, existing *Summary, coveredStart, coveredEnd int, summarized []memory.StoredMessage) {
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
			if hs, ok := m.store.(interface {
				AppendSummaryCheckpoint(string, memory.SummaryCheckpoint) error
			}); ok {
				if appendErr := hs.AppendSummaryCheckpoint(m.sessionKey, memory.SummaryCheckpoint{
					GeneratedAt:     summary.GeneratedAt,
					Model:           summary.Model,
					SourceSeqStart:  summary.LastSummarizedSeqRange().SeqStart,
					SourceSeqEnd:    summary.LastSummarizedSeqRange().SeqEnd,
					CoveredSeqStart: summary.CoveredSeqStart,
					CoveredSeqEnd:   summary.CoveredSeqEnd,
					Summary:         raw,
				}); appendErr != nil {
					logger.WarnCF("llmcontext", "compression: failed to append summary checkpoint", map[string]any{
						"session_key": m.sessionKey,
						"error":       appendErr.Error(),
					})
				}
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

// dropOldestGroups removes the oldest turn groups from conv until the estimated
// token count drops below safetyPercent or conv reaches retainMinMessages.
func (m *Manager) dropOldestGroups(_ context.Context, conv []providers.Message) []providers.Message {
	for {
		if len(conv) <= m.cfg.retainMinMessages {
			break
		}
		tokens := estimateTokens(conv)
		pct := 0.0
		if m.cfg.contextWindow > 0 {
			pct = float64(tokens) * 100 / float64(m.cfg.contextWindow)
		}
		if pct < float64(m.cfg.safetyPercent) {
			break
		}

		// Find the end of the first turn group.
		groupEnd := resolveGroup(conv, 0).end

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

// dropOldestStoredGroups is the seq-preserving equivalent of dropOldestGroups.
func (m *Manager) dropOldestStoredGroups(_ context.Context, conv []memory.StoredMessage) []memory.StoredMessage {
	for {
		if len(conv) <= m.cfg.retainMinMessages {
			break
		}
		plain := storedToPlain(conv)
		tokens := estimateTokens(plain)
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
		msgTokens := estimateTokens([]providers.Message{conv[i]})
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
