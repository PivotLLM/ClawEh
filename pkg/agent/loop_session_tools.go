// ClawEh
// License: MIT

package agent

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"

	"github.com/PivotLLM/ClawEh/pkg/memory"
	"github.com/PivotLLM/ClawEh/pkg/providers"
	"github.com/PivotLLM/ClawEh/pkg/tools"
)

// registerSessionManagementTools wires the compact_session and get_session_info
// tools into each registered agent. These tools require closures over the AgentLoop
// (for context manager access) and cannot be registered in NewAgentInstance.
func (al *AgentLoop) registerSessionManagementTools(registry *AgentRegistry) {
	for _, agentID := range registry.ListAgentIDs() {
		agentInst, ok := registry.GetAgent(agentID)
		if !ok {
			continue
		}

		// Capture loop variable for closures.
		currentAgent := agentInst

		// compact_session: triggers an immediate LLM-based compression pass.
		// Wrap ctx with the agent ID so provider error logs from the
		// compression LLM call attribute correctly when the MCP tool path
		// reaches the context manager outside runAgentLoop.
		compactFn := func(ctx context.Context, sessionKey string) error {
			ctx = providers.WithAgentID(ctx, currentAgent.ID)
			cm, release := al.getContextManager(currentAgent, sessionKey)
			defer release()
			return cm.Compact(ctx)
		}
		currentAgent.Tools.Register(tools.NewSessionCompactTool(compactFn))

		// get_session_info: returns archive bounds, message count, and summary coverage.
		infoFn := func(ctx context.Context, sessionKey string) (*tools.SessionInfo, error) {
			info := &tools.SessionInfo{
				SessionKey: sessionKey,
			}

			// Context manager stats: message count and last compressed at.
			cm, release := al.getContextManager(currentAgent, sessionKey)
			defer release()
			stats := cm.Stats()
			info.ContextMessageCount = stats.TotalMessages
			if !stats.LastCompressedAt.IsZero() {
				t := stats.LastCompressedAt
				info.LastCompressedAt = &t
			}

			// Summary coverage: parse the stored summary JSON for seq range.
			rawSummary := currentAgent.Sessions.GetSummary(sessionKey)
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

			// Archive bounds and start time.
			archivePath := filepath.Join(
				currentAgent.Workspace,
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
					// StartedAt from the first archived message.
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
		currentAgent.Tools.Register(tools.NewSessionInfoTool(infoFn))
	}
}

// sessionKeyToFilename converts a session key to a safe filename component,
// matching the sanitizeKey logic in pkg/memory/jsonl.go.
func sessionKeyToFilename(key string) string {
	s := strings.ReplaceAll(key, ":", "_")
	s = strings.ReplaceAll(s, "/", "_")
	s = strings.ReplaceAll(s, "\\", "_")
	return s
}
