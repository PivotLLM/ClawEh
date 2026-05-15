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
	history := m.store.GetHistory(m.sessionKey)
	var sysMsg *providers.Message
	conversation := history
	if len(history) > 0 && history[0].Role == "system" {
		sys := history[0]
		sysMsg = &sys
		conversation = history[1:]
	}

	if len(conversation) == 0 {
		return nil
	}

	tokensBeforeCompress := estimateTokens(conversation)
	targetPct := float64(m.cfg.normalPercent) * defaultCompressTargetFactor
	budget := m.cfg.contextWindow * m.cfg.retainTokenPercent / 100

	archiveMin, archiveMax := m.archiveWindow()
	existingSummary, _ := unmarshalSummary(m.store.GetSummary(m.sessionKey))

	// Compression loop: iteratively summarize the oldest portion of the
	// conversation until we reach the target percentage or exhaust iterations.
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
		toSummarize := currentConversation[:tailStart]

		if len(toSummarize) == 0 {
			break // tail covers everything; nothing left to summarize
		}

		newSummary, ok := callLLMChain(ctx, clients, latestSummary, toSummarize, archiveMin, archiveMax, aggressive)
		if !ok {
			iterCount++
			continue
		}

		llmSucceeded = true
		latestSummary = newSummary
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
		return nil
	}

	m.persistResult(sysMsg, currentConversation, latestSummary)

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
		m.persistResult(sysMsg, currentConversation, latestSummary)
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

	// Still at or above safety threshold — drop oldest groups.
	currentConversation = m.dropOldestGroups(ctx, currentConversation)
	m.persistResult(sysMsg, currentConversation, latestSummary)
	m.applyLargeMsgChecks(currentConversation)

	// Recheck after drops.
	tokensFinal = estimateTokens(currentConversation)
	if m.cfg.contextWindow > 0 {
		finalPct = float64(tokensFinal) * 100 / float64(m.cfg.contextWindow)
	}
	if finalPct < float64(m.cfg.safetyPercent) {
		m.cooling = false
		m.lastCompressedAt = time.Now()
	}

	return nil
}

// callLLMChain calls each client in order, returning the first successful Summary.
// It returns (nil, false) if all clients fail.
func callLLMChain(
	ctx context.Context,
	clients []LLMClient,
	existing *Summary,
	toSummarize []providers.Message,
	archiveMin, archiveMax int,
	aggressive bool,
) (*Summary, bool) {
	prompt := buildSummarizationPrompt(existing, archiveMin, archiveMax, aggressive)

	var sb strings.Builder
	for _, msg := range toSummarize {
		fmt.Fprintf(&sb, "[%s]: %s\n\n", msg.Role, msg.Content)
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

		summary.GeneratedAt = time.Now()
		return summary, true
	}

	return nil, false
}

// persistResult writes the compressed history and summary to the store and saves.
func (m *Manager) persistResult(sysMsg *providers.Message, conv []providers.Message, summary *Summary) {
	newHistory := make([]providers.Message, 0, len(conv)+1)
	if sysMsg != nil {
		newHistory = append(newHistory, *sysMsg)
	}
	newHistory = append(newHistory, conv...)

	m.store.SetHistory(m.sessionKey, newHistory)

	if summary != nil {
		if data, err := json.Marshal(summary); err == nil {
			m.store.SetSummary(m.sessionKey, string(data))
		}
	}

	_ = m.store.Save(m.sessionKey)
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
