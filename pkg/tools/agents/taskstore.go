// ClawEh
// License: MIT

package agents

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/PivotLLM/ClawEh/pkg/global"
)

// Task statuses persisted in <uuid>-status.json.
const (
	StatusRunning = "running"
	StatusDone    = "done"
	StatusError   = "error"
	// StatusUnknown is never persisted; it is what a status lookup returns when no
	// record exists for a uuid.
	StatusUnknown = "unknown"
)

const (
	tasksSubdir = "tasks"
	statusSuf   = "-status.json"
	resultsSuf  = "-results.json"
	runSuf      = ".run"
)

// interruptionNote is prepended to a task's instructions when the supervisor
// relaunches it after an interruption. Resume re-runs from the beginning.
const interruptionNote = "NOTE: this task was interrupted (process restart or crash) and is being resumed from the beginning. Before repeating any side-effecting work (sending messages, creating or deleting data, etc.), verify what was already completed."

// TaskRecord is the full, replayable record of one callback task. It is
// persisted to <uuid>-status.json in the launcher's workspace.
type TaskRecord struct {
	UUID         string `json:"uuid"`
	Name         string `json:"name"`
	OwnerAgentID string `json:"owner_agent_id"`
	AgentID      string `json:"agent_id"` // target executor; "" = self
	Mode         string `json:"mode"`
	Task         string `json:"task"`
	Model        string `json:"model,omitempty"` // requested model (alias); "" = agent default
	Channel      string `json:"channel"`
	ChatID       string `json:"chat_id"`
	Status       string `json:"status"`
	CreatedAt    string `json:"created_at"`
	StartedAt    string `json:"started_at,omitempty"`
	FinishedAt   string `json:"finished_at,omitempty"`
	Restarts     int    `json:"restarts"`
	RetryAfter   int64  `json:"retry_after"` // unix secs; earliest relaunch
	Error        string `json:"error,omitempty"`
	ResultsPath  string `json:"results_path"`
	// SpawnDepth is the sub-agent depth of the agent that spawned this task (0 =
	// spawned by a primary turn). Persisted so the depth survives the detached
	// async context and process restarts; the worker runs at SpawnDepth+1.
	SpawnDepth int `json:"spawn_depth,omitempty"`
}

// TaskResults is the worker's output payload, persisted to <uuid>-results.json.
type TaskResults struct {
	UUID       string `json:"uuid"`
	Name       string `json:"name"`
	Status     string `json:"status"`
	FinishedAt string `json:"finished_at"`
	Iterations int    `json:"iterations,omitempty"`
	Content    string `json:"content"`
}

// tasksDirFor returns the tasks directory under a workspace.
func tasksDirFor(workspace string) string {
	return filepath.Join(workspace, tasksSubdir)
}

// relResultsPath is the workspace-relative results pointer handed to the agent.
func relResultsPath(uuid string) string {
	return filepath.ToSlash(filepath.Join(tasksSubdir, uuid+resultsSuf))
}

func statusPath(dir, uuid string) string  { return filepath.Join(dir, uuid+statusSuf) }
func resultsPath(dir, uuid string) string { return filepath.Join(dir, uuid+resultsSuf) }
func runPath(dir, uuid string) string     { return filepath.Join(dir, uuid+runSuf) }

// writeJSONAtomic writes v as indented JSON to path via a temp file + rename so
// a crash never leaves a half-written file.
func writeJSONAtomic(path string, v any) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// writeStatus persists a task record.
func writeStatus(dir string, rec *TaskRecord) error {
	return writeJSONAtomic(statusPath(dir, rec.UUID), rec)
}

// readStatus loads a task record by uuid.
func readStatus(dir, uuid string) (*TaskRecord, error) {
	data, err := os.ReadFile(statusPath(dir, uuid))
	if err != nil {
		return nil, err
	}
	var rec TaskRecord
	if err := json.Unmarshal(data, &rec); err != nil {
		return nil, err
	}
	return &rec, nil
}

// writeResults persists the worker output payload.
func writeResults(dir string, res *TaskResults) error {
	return writeJSONAtomic(resultsPath(dir, res.UUID), res)
}

// markRun creates the <uuid>.run liveness marker.
func markRun(dir, uuid string) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	return os.WriteFile(runPath(dir, uuid), []byte(""), 0o644)
}

// clearRun deletes the <uuid>.run marker (idempotent).
func clearRun(dir, uuid string) {
	_ = os.Remove(runPath(dir, uuid))
}

// listRunUUIDs returns the uuids that currently have a .run marker.
func listRunUUIDs(dir string) []string {
	matches, _ := filepath.Glob(filepath.Join(dir, "*"+runSuf))
	out := make([]string, 0, len(matches))
	for _, m := range matches {
		base := filepath.Base(m)
		out = append(out, strings.TrimSuffix(base, runSuf))
	}
	return out
}

// listStatusRecords returns every task record in the directory, newest first.
func listStatusRecords(dir string) []*TaskRecord {
	matches, _ := filepath.Glob(filepath.Join(dir, "*"+statusSuf))
	recs := make([]*TaskRecord, 0, len(matches))
	for _, m := range matches {
		base := filepath.Base(m)
		uuid := strings.TrimSuffix(base, statusSuf)
		if rec, err := readStatus(dir, uuid); err == nil {
			recs = append(recs, rec)
		}
	}
	sort.Slice(recs, func(i, j int) bool {
		return recs[i].CreatedAt > recs[j].CreatedAt
	})
	return recs
}

// nowRFC returns the current time in RFC3339.
func nowRFC() string { return time.Now().UTC().Format(time.RFC3339) }

// nowEpoch returns the current unix time in seconds.
func nowEpoch() int64 { return time.Now().Unix() }

// retryDelaySecs is the supervisor retry cooldown in whole seconds.
func retryDelaySecs() int64 { return int64(global.TaskRetryDelay / time.Second) }

// errResults builds a TaskResults for a failed task.
func errResults(rec *TaskRecord, msg string) *TaskResults {
	return &TaskResults{
		UUID:       rec.UUID,
		Name:       rec.Name,
		Status:     StatusError,
		FinishedAt: rec.FinishedAt,
		Content:    fmt.Sprintf("Error: %s", msg),
	}
}
