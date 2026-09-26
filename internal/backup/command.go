// ClawEh
// License: MIT

package backup

import (
	"bufio"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/PivotLLM/ClawEh/config"
	"github.com/PivotLLM/ClawEh/internal"
)

// NewBackupCommand returns `claw backup [--dest DIR]`: the nightly backup, on
// demand. The gateway may be running; databases are snapshotted with VACUUM
// INTO, which does not disturb a live store.
func NewBackupCommand() *cobra.Command {
	var dest string
	cmd := &cobra.Command{
		Use:   "backup",
		Short: "Write a backup archive of the ClawEh install",
		Long: "Writes claw-backup-<timestamp>.tar.gz containing config.json, the cron jobs\n" +
			"file, state/ (tokens and the device pairing database), credentials.json and\n" +
			"tls/ when present, and every SQLite database under CLAW_HOME (session archives,\n" +
			"cognitive memory, the fusion OAuth store), each checked with quick_check and\n" +
			"copied with VACUUM INTO. Safe to run while the gateway is up. Archives older\n" +
			"than backup.retain_days are pruned from the destination.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runBackup(cmd, dest)
		},
	}
	cmd.Flags().StringVar(&dest, "dest", "", "directory to write the archive to (default: backup.dest, else $CLAW_HOME/backup)")
	return cmd
}

func runBackup(cmd *cobra.Command, dest string) error {
	configPath := internal.GetConfigPath()
	cfg, err := config.LoadConfig(configPath)
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}
	res, err := RunForConfig(cfg, configPath, time.Now(), Options{Dest: dest})
	if err != nil {
		return err
	}
	if _, werr := fmt.Fprintf(cmd.OutOrStdout(), "Wrote %s (%s, %d files)\n", res.Archive, humanBytes(res.Bytes), res.Files); werr != nil {
		return werr
	}
	if len(res.Skipped) == 0 {
		return nil
	}
	for _, s := range res.Skipped {
		if _, werr := fmt.Fprintf(cmd.ErrOrStderr(), "SKIPPED %s: %v\n", s.Path, s.Err); werr != nil {
			return werr
		}
	}
	return fmt.Errorf("%d database(s) failed quick_check and were left out of the archive", len(res.Skipped))
}

// NewRestoreCommand returns `claw restore <archive> [--yes]`.
func NewRestoreCommand() *cobra.Command {
	var yes bool
	cmd := &cobra.Command{
		Use:   "restore <archive>",
		Short: "Restore a backup archive into CLAW_HOME",
		Long: "Restores a claw-backup-*.tar.gz written by `claw backup` or the nightly backup.\n" +
			"The gateway must be stopped. Every file the archive will replace is listed and,\n" +
			"after confirmation, moved to $CLAW_HOME/restore-backup-<timestamp>/ before the\n" +
			"archived copy is put in place. Restored databases are checked with quick_check\n" +
			"first; if any fails the restore aborts and nothing is changed.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runRestore(cmd, args[0], yes)
		},
	}
	cmd.Flags().BoolVar(&yes, "yes", false, "do not ask for confirmation")
	return cmd
}

func runRestore(cmd *cobra.Command, archive string, yes bool) error {
	home := internal.GetClawHome()
	running, err := GatewayRunning(home)
	if err != nil {
		return err
	}
	if running {
		return ErrGatewayRunning
	}
	plan, err := PlanRestore(archive, home)
	if err != nil {
		return err
	}
	out := cmd.OutOrStdout()
	taken := plan.Manifest.CreatedAt.Local().Format(time.RFC3339) //nolint:gosmopolitan // the operator reads the time in their own zone
	var b strings.Builder
	fmt.Fprintf(&b, "Archive:   %s (taken %s from %s)\n", plan.Archive, taken, plan.Manifest.Home)
	fmt.Fprintf(&b, "Restoring into %s\n", plan.Home)
	if plan.Manifest.AgentsDir != "" {
		fmt.Fprintf(&b, "Agent workspaces into %s\n", plan.Manifest.AgentsDir)
	}
	replaced := 0
	for _, e := range plan.Entries {
		tag := "new     "
		if e.Replaces {
			tag = "REPLACE "
			replaced++
		}
		fmt.Fprintf(&b, "  %s %s (%s)\n", tag, e.Target, humanBytes(e.Size))
	}
	fmt.Fprintf(&b, "%d files, %d replaced; replaced files are kept under %s\n",
		len(plan.Entries), replaced, filepath.Join(home, restoreBackupPrefix+"<timestamp>"))
	if _, werr := fmt.Fprint(out, b.String()); werr != nil {
		return werr
	}
	if !yes {
		if _, werr := fmt.Fprint(out, "Type 'yes' to continue: "); werr != nil {
			return werr
		}
		line, rerr := bufio.NewReader(cmd.InOrStdin()).ReadString('\n')
		if rerr != nil && line == "" {
			return errors.New("no confirmation read; re-run with --yes")
		}
		if strings.TrimSpace(line) != "yes" {
			return errors.New("restore cancelled")
		}
	}
	parked, err := plan.Apply(time.Now())
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(out, "Restored %d files. Previous files are in %s\n", len(plan.Entries), parked)
	return err
}

// humanBytes renders a size as B, KiB, MiB or GiB.
func humanBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	v, suffix := float64(n)/unit, "KiB"
	if v >= unit {
		v, suffix = v/unit, "MiB"
	}
	if v >= unit {
		v, suffix = v/unit, "GiB"
	}
	return fmt.Sprintf("%.1f %s", v, suffix)
}
