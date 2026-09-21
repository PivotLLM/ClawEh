// ClawEh
// License: MIT

package audit

import (
	"fmt"
	"path/filepath"

	"github.com/PivotLLM/ClawEh/config"
)

func days(n int, zero string) string {
	if n <= 0 {
		return zero
	}
	return itoa(n) + " days"
}

func collectData(cfg *config.Config, env Environment) Section {
	dd := dataDir(cfg, env)
	lg := cfg.Logging
	d := cfg.Agents.Defaults
	mem := d.Memory.Retention
	mc := cfg.Tools.MediaCleanup
	bh, bm := cfg.Backup.BackupAt()

	t := Table{Caption: "Storage", Columns: []string{"Data", "Location", "Settings"}}
	t.Rows = append(t.Rows,
		row("Config file", orValue(env.ConfigPath, unknown), "holds API keys and tokens in plain text"),
		row("Data directory", dd, ""),
		row("Logs", filepath.Join(dd, "logs"),
			"file "+onOff(lg.File)+", console "+onOff(lg.Console)+", level "+orValue(lg.Level, unknown)+
				", json "+onOff(lg.JSON)+", retention "+days(lg.RetentionDays, "forever")),
	)
	content := "off"
	if lg.LogMessageContent {
		content = "ON: inbound message text and API request/response bodies are written to the log"
		t.Highlight = append(t.Highlight, len(t.Rows))
	}
	t.Rows = append(t.Rows, row("Message content logging", filepath.Join(dd, "logs"), content))
	dumps := "refusals " + onOff(lg.DumpRefusals) + ", every response " + onOff(lg.DumpAll) +
		", failed compressions " + onOff(lg.DumpFailedCompressions)
	if lg.DumpAll {
		dumps = "EVERY LLM request and response is written to disk; " + dumps
		t.Highlight = append(t.Highlight, len(t.Rows))
	}
	t.Rows = append(t.Rows,
		row("LLM dumps (full prompts and replies)", filepath.Join(dd, "logs", "dumps"), dumps),
		row("Agent workspaces", cfg.BaseDir(), "one directory per agent: files/, skills/, tasks/, tmp/, sessions/, cogmem/"),
		row("Session archives", "<workspace>/sessions",
			"archive after "+days(d.ArchiveDays, "never by age")+" or "+itoa(d.ArchiveMessageCount)+" messages; "+
				"summaries kept "+days(d.SummaryRetentionDays, "forever")+", max "+itoa(d.SummaryMaxCount)+
				"; archived content capped at "+itoa(d.ArchiveContentMaxBytes)+" bytes (0 = unlimited)"),
		row("Cognitive memory", "<workspace>/cogmem",
			"event memories kept "+days(mem.EffectiveEventDays(), "forever")+", retired memories kept "+
				days(mem.EffectiveRetiredDays(), "forever")+"; export "+onOff(d.Memory.Export.Enabled)),
		row("Shared common directory", cfg.ResolveCommonDir(), "readable and writable by every agent that shares it"),
		row("Skills", cfg.SkillsPath(), ""),
		row("Cron store", filepath.Join(cfg.CronPath(), "jobs.json"), ""),
		row("Token and pairing state", filepath.Join(dd, "state"), "service tokens, integration tokens, device pairing database"),
		row("Fusion", cfg.FusionPath(), "REST-API tool definitions; OAuth tokens in "+cfg.FusionTokensPath()),
		row("Media store", "in-process registry of tool-produced files",
			"cleanup "+onOff(mc.Enabled)+", max age "+itoa(mc.MaxAge)+" min, every "+itoa(mc.Interval)+" min"),
		row("Backups", filepath.Join(dd, "backup"),
			onOff(cfg.Backup.IsEnabled())+fmt.Sprintf(", daily at %02d:%02d, retain %d days", bh, bm, cfg.Backup.BackupRetainDays())),
		row("External-message prefix (security.message_prefix)", "", prefixState(cfg.Security.MessagePrefix)),
	)

	return Section{
		Title: "Data",
		Notes: []string{
			"Where ClawEh keeps what it produces, and the retention that applies. Highlighted rows write " +
				"conversation content to disk beyond the session archive.",
		},
		Tables: []Table{t},
	}
}

func prefixState(p string) string {
	if p == "" {
		return "default prefix"
	}
	return "custom prefix (" + itoa(len(p)) + " characters)"
}
