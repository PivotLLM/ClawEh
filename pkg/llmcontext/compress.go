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
	var sysMsg *providers.Message
	storedConversation := storedHistory
	if len(storedHistory) > 0 && storedHistory[0].Role == "system" {
		sys := storedHistory[0].Message
		sysMsg = &sys
		storedConversation = storedHistory[1:]
	}

	if len(storedConversation) == 0 {
		return nil
	}

	// Derive plain providers.Message slices for token estimation, tail selection,
	// and history manipulation — these do not need seq numbers.
	toPlain := func(stored []memory.StoredMessage) []providers.Message {
		msgs := make([]providers.Message, len(stored))
		for i, sm := range stored {
			msgs[i] = sm.Message
		}
		return msgs
	}

	conversation := toPlain(storedConversation)
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

		// Strip any seq references outside the valid range before accepting the summary.
		newSummary.StripOutOfRangeSeqRefs(newSummary.CoveredSeqStart, archiveMax)

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
		return m.handleNormalPostLoop(ctx, sysMsg, conversation, currentConversation, latestSummary, llmSucceeded, tokensBeforeCompress, targetPct)
	}
	return m.handleSafetyNetPostLoop(ctx, sysMsg, conversation, currentConversation, latestSummary, existingSummary, llmSucceeded)
}

// handleNormalPostLoop handles post-loop logic for the normal (non-safety-net) path.
func (m *Manager) handleNormalPostLoop(
	ctx context.Context,
	sysMsg *providers.Message,
	originalConversation []providers.Message,
	currentConversation []providers.Message,
	latestSummary *Summary,
	llmSucceeded bool,
	tokensBeforeCompress int,
	targetPct float64,
) error {
	_ = ctx // reserved for future use
	if !llmSucceeded {
		logger.WarnCF("llmcontext", "compression failed: no LLM client succeeded", map[string]any{
			"session_key": m.sessionKey,
		})
		return ErrCompressionFailed
	}

	if err := m.persistResult(sysMsg, currentConversation, latestSummary); err != nil {
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
	sysMsg *providers.Message,
	originalConversation []providers.Message,
	currentConversation []providers.Message,
	latestSummary *Summary,
	existingSummary *Summary,
	llmSucceeded bool,
) error {
	if llmSucceeded {
		if err := m.persistResult(sysMsg, currentConversation, latestSummary); err != nil {
			return err
		}
	} else if existingSummary != nil {
		logger.WarnCF("llmcontext", "safety-net compression: all LLM clients failed; using stale summary", map[string]any{
			"session_key":   m.sessionKey,
			"stale_summary": true,
		})
		currentConversation = originalConversation
	} else {
		logger.WarnCF("llmcontext", "safety-net compression: all LLM clients failed; no summary available", map[string]any{
			"session_key": m.sessionKey,
			"no_summary":  true,
		})
		currentConversation = originalConversation
	}

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
	// message checks, then persist. Order: group-drop → applyLargeMsgChecks →
	// persistResult → compute finalPct.
	currentConversation = m.dropOldestGroups(ctx, currentConversation)
	m.applyLargeMsgChecks(currentConversation)
	if err := m.persistResult(sysMsg, currentConversation, latestSummary); err != nil {
		return err
	}

	// Recheck after drops.
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

	var sb strings.Builder
	for _, sm := range toSummarize {
		fmt.Fprintf(&sb, "[#%d] [%s]: %s\n\n", sm.Seq, sm.Role, sm.Content)
	}
	formatted := sb.String()

	messages := []providers.Message{
		{Role: "user", Content: prompt + "\n\n---\n\n" + formatted},
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

		// Set coverage from the actual seq range of messages passed to the LLM.
		// Do NOT use any seq values emitted by the LLM itself.
		summary.CoveredSeqStart = coveredStart
		summary.CoveredSeqEnd = coveredEnd
		if len(toSummarize) > 0 {
			summary.CoveredSeqStartAt = toSummarize[0].CreatedAt
			summary.CoveredSeqEndAt = toSummarize[len(toSummarize)-1].CreatedAt
		}
		summary.GeneratedAt = time.Now()
		return summary, true
	}

	return nil, false
}

// persistResult writes the compressed history and summary to the store and saves.
// It returns ErrCompressionFailed if Save() fails.
// After a successful save it persists compaction state if the store supports it.
func (m *Manager) persistResult(sysMsg *providers.Message, conv []providers.Message, summary *Summary) error {
	newHistory := make([]providers.Message, 0, len(conv)+1)
	if sysMsg != nil {
		newHistory = append(newHistory, *sysMsg)
	}
	newHistory = append(newHistory, conv...)

	m.store.SetHistory(m.sessionKey, newHistory)

	summaryModel := ""
	if summary != nil {
		if data, err := json.Marshal(summary); err == nil {
			m.store.SetSummary(m.sessionKey, string(data))
		}
		summaryModel = summary.Model
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
			SummaryGeneratedAt:          m.lastCompressedAt,
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
