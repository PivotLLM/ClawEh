// ClawEh - Cognitive Memory CLI
// License: MIT

// Package memory provides the `claw memory` maintenance subcommands.
package memory

import (
	"context"
	"fmt"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/PivotLLM/ClawEh/internal"
	"github.com/PivotLLM/ClawEh/pkg/cogmem/store"
)

// NewMemoryCommand builds the `claw memory` command group.
func NewMemoryCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "memory",
		Short: "Maintain cognitive memory (cogmem) across all assistants",
	}
	cmd.AddCommand(newPurgeCommand())
	return cmd
}

func newPurgeCommand() *cobra.Command {
	var confirm bool
	cmd := &cobra.Command{
		Use:   "purge",
		Short: "Delete archived domains and non-active memories for all assistants",
		Long: "Purges everything that is not current active memory from every " +
			"assistant's cognitive-memory databases: each domain whose status is " +
			"not active (and all of its memories), plus every non-active memory " +
			"(retired, superseded, review). Active memories in active domains are " +
			"kept.\n\nWithout --confirm this is a DRY RUN that only reports what " +
			"would be removed.",
		Args: cobra.NoArgs,
		RunE: func(_ *cobra.Command, _ []string) error {
			return runPurge(confirm)
		},
	}
	cmd.Flags().BoolVar(&confirm, "confirm", false, "Actually delete (default is a dry run that only reports counts)")
	return cmd
}

func runPurge(confirm bool) error {
	cfg, err := internal.LoadConfig()
	if err != nil {
		return err
	}

	var dbs []string
	for _, dir := range cfg.AgentSessionDirs() {
		matches, _ := filepath.Glob(filepath.Join(dir, "*.cogmem.db"))
		dbs = append(dbs, matches...)
	}
	if len(dbs) == 0 {
		fmt.Println("No cognitive-memory databases found.")
		return nil
	}

	if confirm {
		fmt.Printf("PURGING non-active memory across %d database(s)\n", len(dbs))
	} else {
		fmt.Printf("DRY RUN across %d database(s) — re-run with --confirm to delete\n", len(dbs))
	}

	ctx := context.Background()
	var totMem, totDom int64
	for _, path := range dbs {
		s, err := store.Open(path)
		if err != nil {
			fmt.Printf("  %s: open error: %v\n", path, err)
			continue
		}
		st, perr := s.PurgeNonActive(ctx, confirm)
		if perr == nil && confirm && (st.Memories > 0 || st.Domains > 0) {
			_ = s.Vacuum(ctx)
		}
		_ = s.Close()
		if perr != nil {
			fmt.Printf("  %s: error: %v\n", path, perr)
			continue
		}
		totMem += st.Memories
		totDom += st.Domains
		if st.Memories > 0 || st.Domains > 0 {
			fmt.Printf("  %s: %s %d memory(ies), %d domain(s)\n", path, removedOrWould(confirm), st.Memories, st.Domains)
		}
	}

	fmt.Printf("%s %d memory(ies) and %d domain(s) across %d database(s).\n",
		removedOrWouldCap(confirm), totMem, totDom, len(dbs))
	return nil
}

func removedOrWould(confirm bool) string {
	if confirm {
		return "removed"
	}
	return "would remove"
}

func removedOrWouldCap(confirm bool) string {
	if confirm {
		return "Removed"
	}
	return "Would remove"
}
