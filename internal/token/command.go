// ClawEh
// License: MIT

// Package token implements the `claw token` CLI: mint, rotate, revoke, and list
// long-lived per-agent MCP service tokens (see docs/service-tokens.md).
package token

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/PivotLLM/ClawEh/internal"
	"github.com/PivotLLM/ClawEh/pkg/config"
	"github.com/PivotLLM/ClawEh/pkg/routing"
	"github.com/PivotLLM/ClawEh/pkg/servicetoken"
)

// NewTokenCommand returns the `claw token` command group.
func NewTokenCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "token",
		Short: "Manage long-lived per-agent MCP service tokens",
		Long: "Mint, rotate, revoke, and list long-lived service tokens. A service token lets an\n" +
			"external MCP client drive an agent's tools (e.g. Maestro) on a stable, headless\n" +
			"credential — see docs/service-tokens.md. A running gateway picks up changes\n" +
			"automatically within a few seconds.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error { return cmd.Help() },
	}
	cmd.AddCommand(newIssueCommand(), newRotateCommand(), newRevokeCommand(), newListCommand())
	return cmd
}

func newIssueCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "issue <agent>",
		Short: "Mint (or replace) and print an agent's service token",
		Args:  cobra.ExactArgs(1),
		RunE:  func(_ *cobra.Command, args []string) error { return issue(args[0]) },
	}
}

func newRotateCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "rotate <agent>",
		Short: "Replace an agent's service token (alias for issue)",
		Args:  cobra.ExactArgs(1),
		RunE:  func(_ *cobra.Command, args []string) error { return issue(args[0]) },
	}
}

func newRevokeCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "revoke <agent>",
		Short: "Remove an agent's service token",
		Args:  cobra.ExactArgs(1),
		RunE:  func(_ *cobra.Command, args []string) error { return revoke(args[0]) },
	}
}

func newListCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List agents that have a service token (tokens are not shown)",
		Args:  cobra.NoArgs,
		RunE:  func(_ *cobra.Command, _ []string) error { return list() },
	}
}

// resolveAgent loads config and returns the canonical (normalized) agent ID,
// erroring if the agent is not configured.
func resolveAgent(arg string) (cfg *config.Config, agentID string, err error) {
	cfg, err = internal.LoadConfig()
	if err != nil {
		return nil, "", fmt.Errorf("load config: %w", err)
	}
	want := routing.NormalizeAgentID(arg)
	for i := range cfg.Agents.List {
		if routing.NormalizeAgentID(cfg.Agents.List[i].ID) == want {
			return cfg, want, nil
		}
	}
	return nil, "", fmt.Errorf("no agent %q in config (agents: %s)", arg, agentIDs(cfg))
}

func issue(arg string) error {
	cfg, agentID, err := resolveAgent(arg)
	if err != nil {
		return err
	}
	path := servicetoken.Path(cfg.DataDir())
	tokens, err := servicetoken.Load(path)
	if err != nil {
		return err
	}
	tok, err := servicetoken.Generate()
	if err != nil {
		return err
	}
	tokens[agentID] = tok
	if err := servicetoken.Save(path, tokens); err != nil {
		return err
	}
	fmt.Printf("Service token for agent %q (store it securely; it is not shown again):\n\n  %s\n\n", agentID, tok)
	fmt.Println("Use it as an Authorization: Bearer header on /mcp, or as the session_token")
	fmt.Println("parameter on /internal. A running gateway picks it up automatically within a few seconds.")
	return nil
}

func revoke(arg string) error {
	cfg, agentID, err := resolveAgent(arg)
	if err != nil {
		return err
	}
	path := servicetoken.Path(cfg.DataDir())
	tokens, err := servicetoken.Load(path)
	if err != nil {
		return err
	}
	if _, ok := tokens[agentID]; !ok {
		fmt.Printf("No service token for agent %q.\n", agentID)
		return nil
	}
	delete(tokens, agentID)
	if err := servicetoken.Save(path, tokens); err != nil {
		return err
	}
	fmt.Printf("Revoked service token for agent %q. A running gateway removes it automatically within a few seconds.\n", agentID)
	return nil
}

func list() error {
	cfg, err := internal.LoadConfig()
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}
	tokens, err := servicetoken.Load(servicetoken.Path(cfg.DataDir()))
	if err != nil {
		return err
	}
	ids := servicetoken.Agents(tokens)
	if len(ids) == 0 {
		fmt.Println("No service tokens issued.")
		return nil
	}
	fmt.Println("Agents with a service token:")
	for _, id := range ids {
		fmt.Printf("  %s\n", id)
	}
	return nil
}

func agentIDs(cfg *config.Config) string {
	ids := make([]string, 0, len(cfg.Agents.List))
	for i := range cfg.Agents.List {
		ids = append(ids, cfg.Agents.List[i].ID)
	}
	return strings.Join(ids, ", ")
}
