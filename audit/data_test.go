// ClawEh
// License: MIT

package audit

import (
	"path/filepath"
	"testing"
)

func TestCollectData_ContentLoggingCalledOut(t *testing.T) {
	cfg, env := fixtureConfig(t)
	tb := findTable(t, collectData(t.Context(), cfg, env), "Storage")
	i, content := findRow(t, tb, "Message content logging")
	if content[2] != "off" || isHighlighted(tb, i) {
		t.Errorf("content logging off: row=%v highlighted=%v", content, isHighlighted(tb, i))
	}
	_, logs := findRow(t, tb, "Logs")
	if logs[1] != filepath.Join(env.DataDir, "logs") {
		t.Errorf("logs location = %q", logs[1])
	}

	cfg.Logging.LogMessageContent = true
	cfg.Logging.DumpAll = true
	tb = findTable(t, collectData(t.Context(), cfg, env), "Storage")
	i, content = findRow(t, tb, "Message content logging")
	if !isHighlighted(tb, i) {
		t.Error("content logging on must be highlighted")
	}
	contains(t, content[2], "ON: inbound message text", "content logging wording")
	i, dumps := findRow(t, tb, "LLM dumps")
	if !isHighlighted(tb, i) || dumps[1] != filepath.Join(env.DataDir, "logs", "dumps") {
		t.Errorf("dumps row = %v highlighted=%v", dumps, isHighlighted(tb, i))
	}
	contains(t, dumps[2], "EVERY LLM request and response", "dump_all wording")

	_, cfgRow := findRow(t, tb, "Config file")
	if cfgRow[1] != env.ConfigPath {
		t.Errorf("config path = %q", cfgRow[1])
	}
	_, backup := findRow(t, tb, "Backups")
	if backup[1] != filepath.Join(env.DataDir, "backup") || backup[2] != "on, daily at 03:00, retain 30 days" {
		t.Errorf("backup row = %v", backup)
	}
	_, mem := findRow(t, tb, "Cognitive memory")
	contains(t, mem[2], "event memories kept 30 days, retired memories kept 90 days", "memory retention defaults")
}
