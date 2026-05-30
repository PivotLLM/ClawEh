// ClawEh
// License: MIT

package agent

import (
	"encoding/json"
	"path/filepath"
	"strings"

	"github.com/PivotLLM/ClawEh/pkg/memory"
	"github.com/PivotLLM/ClawEh/pkg/tools"
)

// buildSessionInfo constructs session info for the given agent and session key.
// Extracted so it can be reused by registerRuntimeTools in loop.go.
func buildSessionInfo(al *AgentLoop, agent *AgentInstance, sessionKey string) (*tools.SessionInfo, error) {
	info := &tools.SessionInfo{
		SessionKey: sessionKey,
	}

	cm, release := al.getContextManager(agent, sessionKey)
	defer release()
	stats := cm.Stats()
	info.ContextMessageCount = stats.TotalMessages
	if !stats.LastCompressedAt.IsZero() {
		t := stats.LastCompressedAt
		info.LastCompressedAt = &t
	}

	rawSummary := agent.Sessions.GetSummary(sessionKey)
	if rawSummary != "" {
		var sv struct {
			CoveredSeqStart int `json:"covered_seq_start"`
			CoveredSeqEnd   int `json:"covered_seq_end"`
		}
		if err := json.Unmarshal([]byte(rawSummary), &sv); err == nil && sv.CoveredSeqStart > 0 {
			info.SummaryCovers = &tools.SummaryCoverage{
				SeqStart: sv.CoveredSeqStart,
				SeqEnd:   sv.CoveredSeqEnd,
			}
			if !stats.LastCompressedAt.IsZero() {
				t := stats.LastCompressedAt
				info.SummaryCovers.GeneratedAt = &t
			}
		}
	}

	archivePath := filepath.Join(
		agent.Workspace,
		"sessions",
		sessionKeyToFilename(sessionKey)+".archive.db",
	)
	if a, openErr := memory.OpenReadOnly(archivePath); openErr == nil {
		defer a.Close()
		minSeq, maxSeq, boundsErr := a.Bounds()
		if boundsErr == nil {
			info.ArchiveMinSeq = minSeq
			info.ArchiveMaxSeq = maxSeq
			if maxSeq >= minSeq && minSeq > 0 {
				info.TotalArchived = maxSeq - minSeq + 1
			}
			if minSeq > 0 {
				if startMsgs, err := a.QueryRange(minSeq, minSeq); err == nil && len(startMsgs) > 0 {
					t := startMsgs[0].CreatedAt
					info.StartedAt = &t
				}
			}
		}
	}

	return info, nil
}

// sessionKeyToFilename converts a session key to a safe filename component,
// matching the sanitizeKey logic in pkg/memory/jsonl.go.
func sessionKeyToFilename(key string) string {
	s := strings.ReplaceAll(key, ":", "_")
	s = strings.ReplaceAll(s, "/", "_")
	s = strings.ReplaceAll(s, "\\", "_")
	return s
}
