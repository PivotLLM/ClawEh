// ClawEh
// License: MIT

package audit

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/PivotLLM/ClawEh/config"
	"github.com/PivotLLM/ClawEh/cron"
)

// payloadPreview is how much of a job's message the report shows.
const payloadPreview = 80

func scheduleText(s cron.CronSchedule) string {
	switch {
	case s.Kind == "every" && s.EveryMS != nil:
		return fmt.Sprintf("every %ds", *s.EveryMS/1000)
	case s.Kind == "cron":
		if s.TZ != "" {
			return s.Expr + " (" + s.TZ + ")"
		}
		return s.Expr
	case s.Kind == cron.KindListen:
		return "listen (continuous)"
	case s.AtMS != nil:
		return "once at " + msTime(*s.AtMS)
	default:
		return "one-time"
	}
}

func payloadText(p cron.CronPayload) string {
	body := p.Message
	switch {
	case p.Command != "":
		body = p.Command
	case p.Watch != nil:
		body = "watch " + p.Watch.Tool + ": " + p.Message
	}
	body = strings.Join(strings.Fields(body), " ")
	if len(body) > payloadPreview {
		body = body[:payloadPreview] + "..."
	}
	return p.Mode + ": " + body
}

func deliveryText(p cron.CronPayload) string {
	if p.Channel == "" && p.To == "" {
		return "agent's default channel"
	}
	s := p.Channel
	if p.To != "" {
		s += " " + p.To
	}
	if p.PeerKind == "direct" {
		s += " (direct)"
	}
	return s
}

// cronRows reads the job store (the file `claw cron list` and the gateway
// read) without starting the scheduler.
func cronRows(path string) [][]string {
	if _, err := os.Stat(path); err != nil {
		if os.IsNotExist(err) {
			return [][]string{row("(none: no cron store at "+path+")", "", "", "", "", "")}
		}
		return [][]string{row("unavailable: "+err.Error(), "", "", "", "", "")}
	}
	cs := cron.NewCronService(path, nil)
	if err := cs.Load(); err != nil {
		return [][]string{row("unavailable: "+err.Error(), "", "", "", "", "")}
	}
	jobs := cs.ListJobs(true)
	if len(jobs) == 0 {
		return [][]string{row(none, "", "", "", "", "")}
	}
	rows := make([][]string, 0, len(jobs))
	for _, j := range jobs {
		rows = append(rows, row(orValue(j.Name, j.ID), scheduleText(j.Schedule), orValue(j.AgentID, "(operator)"),
			yesNo(j.Enabled), payloadText(j.Payload), deliveryText(j.Payload)))
	}
	return rows
}

func collectScheduled(cfg *config.Config, env Environment) Section {
	cronPath := filepath.Join(dataDir(cfg, env), "cron", "jobs.json")
	jobs := Table{
		Caption: "Cron jobs",
		Columns: []string{"Job", "Schedule", "Agent", "Enabled", "Payload", "Delivery"},
		Rows:    cronRows(cronPath),
	}

	maestro := Table{Caption: "Maestro task orchestration", Columns: []string{"Agent", "Enabled", "Max concurrent", "Parallel runs"}}
	for _, a := range enabledAgents(cfg) {
		if !a.MaestroEnabled() {
			maestro.Rows = append(maestro.Rows, row(a.ID, "no", "", ""))
			continue
		}
		mc := "Maestro default (5)"
		if a.Maestro.MaxConcurrent > 0 {
			mc = itoa(a.Maestro.MaxConcurrent)
		}
		par := "allowed when requested"
		if !a.Maestro.ParallelAllowed() {
			par = "never (sequential only)"
		}
		maestro.Rows = append(maestro.Rows, row(a.ID, "yes", mc, par))
	}
	if len(maestro.Rows) == 0 {
		maestro.Rows = append(maestro.Rows, row("(no enabled agent)", "", "", ""))
	}

	d := cfg.Agents.Defaults
	cons := d.Memory.Consolidation
	consolidation := "every " + itoa(cons.EveryNMessages) + " messages or " + itoa(cons.IdleMinutes) + " idle minutes"
	if cons.Nightly {
		consolidation += ", nightly at " + orValue(cons.NightlyAt, unknown)
	}
	bh, bm := cfg.Backup.BackupAt()
	background := Table{Caption: "Background activity", Columns: []string{"Task", "Schedule"}}
	background.Rows = append(background.Rows,
		row("Memory consolidation (agents with cognitive memory)", consolidation),
		row("Backup", onOff(cfg.Backup.IsEnabled())+fmt.Sprintf(", daily at %02d:%02d", bh, bm)),
		row("Log roll", "local midnight"),
		row("Media cleanup", onOff(cfg.Tools.MediaCleanup.Enabled)+", every "+itoa(cfg.Tools.MediaCleanup.Interval)+" min"),
		row("Config reload check", "every "+cfg.ConfigReloadInterval().String()),
	)

	return Section{
		Title: "Scheduled activity",
		Notes: []string{"What runs without a user message: cron jobs (from " + cronPath +
			"), Maestro task runs, and ClawEh's own background work."},
		Tables: []Table{jobs, maestro, background},
	}
}
