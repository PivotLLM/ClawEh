// ClawEh - Session store CLI
// License: MIT

// Package sessions provides the `claw sessions` maintenance subcommands.
package sessions

import (
	"fmt"
	"io"
	"os"
	"sort"

	"github.com/PivotLLM/ctxengine/session"
	"github.com/spf13/cobra"

	"github.com/PivotLLM/ClawEh/internal"
	"github.com/PivotLLM/ClawEh/internal/pidfile"
)

// NewSessionsCommand builds the `claw sessions` command group.
func NewSessionsCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "sessions",
		Short: "Maintain session stores across all assistants",
	}
	cmd.AddCommand(newMigrateCommand())
	cmd.AddCommand(newEraseCommand())
	return cmd
}

func newEraseCommand() *cobra.Command {
	var req EraseRequest
	cmd := &cobra.Command{
		Use:   "erase",
		Short: "Delete every session archive belonging to one sender on one channel",
		Long: "Deletes, across all assistants, each session whose key belongs to the " +
			"given channel and chat (or sender) id: direct sessions under the per-user, " +
			"per-platform and per-account scopes, group and channel sessions with that " +
			"peer id, and device sessions keyed by that device id. Under the unified " +
			"scope a sender's messages live in the assistant's shared main session, " +
			"which has no per-sender column; it is reported, and deleted only with " +
			"--all. Cognitive memories are not touched: cogmem records no per-sender " +
			"provenance.\n\n" +
			"Run with the service stopped; the command refuses to run while the " +
			"gateway is up (use DELETE /api/sessions?channel=&chat_id= there).",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runErase(cmd, req)
		},
	}
	cmd.Flags().StringVar(&req.Channel, "channel", "", "Channel name as it appears in session keys (telegram, slack, webui, device, ...)")
	cmd.Flags().StringVar(&req.ChatID, "chat", "", "Chat or sender id as the channel reports it (the peer id ending the session key)")
	cmd.Flags().BoolVar(&req.All, "all", false, "Also delete the shared main session (unified scope): every sender's history in it")
	// MarkFlagRequired only fails when the flag does not exist, which is a
	// programming error in the lines above, so it is fatal at construction.
	for _, name := range []string{"channel", "chat"} {
		if err := cmd.MarkFlagRequired(name); err != nil {
			panic(fmt.Sprintf("sessions erase: mark flag %q required: %v", name, err))
		}
	}
	return cmd
}

func runErase(cmd *cobra.Command, req EraseRequest) error {
	cfg, err := internal.LoadConfig()
	if err != nil {
		return err
	}
	if pid, running := pidfile.Read(cfg.DataDir()); running {
		return fmt.Errorf("%s is running (pid %d); stop the service before erasing sessions, or use DELETE /api/sessions", internal.BinaryName, pid)
	}
	rep, err := Erase(cfg, req, nil)
	if _, werr := fmt.Fprint(cmd.OutOrStdout(), FormatReport(rep, req)); werr != nil {
		return werr
	}
	return err
}

func newMigrateCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "migrate",
		Short: "Fold legacy .jsonl/.meta.json session files into each session's archive DB",
		Long: "Folds every <key>.jsonl + <key>.meta.json pair in each assistant's " +
			"sessions directory into that session's <key>.archive.db, which now " +
			"holds the live window and the session state alongside the archive. " +
			"Sources are renamed *.migrated and can be deleted once verified. " +
			"Sessions already migrated are skipped.\n\n" +
			"Run once, with the service stopped; the command refuses to run " +
			"while the gateway is up.",
		Args: cobra.NoArgs,
		RunE: func(_ *cobra.Command, _ []string) error {
			return runMigrate()
		},
	}
}

func runMigrate() error {
	cfg, err := internal.LoadConfig()
	if err != nil {
		return err
	}
	if pid, running := pidfile.Read(cfg.DataDir()); running {
		return fmt.Errorf("%s is running (pid %d); stop the service before migrating sessions", internal.BinaryName, pid)
	}
	return migrateDirs(os.Stdout, cfg.AgentSessionDirs())
}

// migrateDirs runs the migration over every existing sessions directory,
// printing one line per session and a totals line. It returns an error when
// any session or directory failed, so the process exits non-zero.
func migrateDirs(w io.Writer, dirs []string) error {
	var migrated, skipped, failed int
	for _, dir := range dirs {
		if _, err := os.Stat(dir); err != nil {
			continue // no sessions directory yet — nothing to migrate
		}
		report, err := session.MigrateJSONL(dir)
		if err != nil {
			if _, werr := fmt.Fprintf(w, "%s: error: %v\n", dir, err); werr != nil {
				return werr
			}
			failed++
			continue
		}
		for _, key := range sorted(report.Migrated) {
			if _, werr := fmt.Fprintf(w, "%s: migrated\n", key); werr != nil {
				return werr
			}
		}
		for _, key := range sorted(report.Skipped) {
			if _, werr := fmt.Fprintf(w, "%s: skipped (already migrated)\n", key); werr != nil {
				return werr
			}
		}
		errKeys := make([]string, 0, len(report.Errors))
		for key := range report.Errors {
			errKeys = append(errKeys, key)
		}
		for _, key := range sorted(errKeys) {
			if _, werr := fmt.Fprintf(w, "%s: error: %v\n", key, report.Errors[key]); werr != nil {
				return werr
			}
		}
		migrated += len(report.Migrated)
		skipped += len(report.Skipped)
		failed += len(report.Errors)
	}

	if _, werr := fmt.Fprintf(w, "Migrated %d, skipped %d, errors %d.\n", migrated, skipped, failed); werr != nil {
		return werr
	}
	if failed > 0 {
		return fmt.Errorf("%d session(s) failed to migrate", failed)
	}
	return nil
}

func sorted(keys []string) []string {
	out := append([]string(nil), keys...)
	sort.Strings(out)
	return out
}
