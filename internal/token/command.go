// ClawEh
// License: MIT

// Package token implements the `claw token` CLI: mint, rotate, revoke, and list
// long-lived per-agent MCP service tokens (see docs/service-tokens.md).
package token

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/PivotLLM/ClawEh/config"
	"github.com/PivotLLM/ClawEh/internal"
	"github.com/PivotLLM/ClawEh/routing"
	"github.com/PivotLLM/ClawEh/servicetoken"
)

// NewTokenCommand returns the `claw token` command group.
func NewTokenCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "token",
		Short: "Manage long-lived per-agent MCP service tokens",
		Long: "Mint, rotate, revoke, and list long-lived service tokens. A service token lets an\n" +
			"external MCP client drive an agent's tools (e.g. Maestro) on a stable, headless\n" +
			"credential — see docs/service-tokens.md. A running claw picks up changes\n" +
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
	if configuredAgent(cfg, want) {
		return cfg, want, nil
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
	// Only the hash goes to disk; the plaintext below is the one time it is shown.
	tokens[agentID] = servicetoken.Hash(tok)
	if err := servicetoken.Save(path, tokens); err != nil {
		return err
	}
	fmt.Printf("Service token for agent %q (store it securely; it is not shown again):\n\n  %s\n\n", agentID, tok)
	fmt.Println("Use it as an Authorization: Bearer header on /mcp, or as the session_token")
	fmt.Println("parameter on /internal. A running claw picks it up automatically within a few seconds.")
	return nil
}

// revoke removes an agent's service token. It works from the token store,
// not the config, so the token of an agent that has been removed from the
// config can still be revoked; otherwise it would work again if an agent with
// that id came back.
func revoke(arg string) error {
	cfg, err := internal.LoadConfig()
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}
	want := routing.NormalizeAgentID(arg)
	path := servicetoken.Path(cfg.DataDir())
	tokens, err := servicetoken.Load(path)
	if err != nil {
		return err
	}
	key, found := "", false
	for id := range tokens {
		if config.SameAgentID(id, want) {
			key, found = id, true
			break
		}
	}
	if !found {
		if !configuredAgent(cfg, want) {
			return fmt.Errorf("no agent %q in config and no service token for it (agents: %s)", arg, agentIDs(cfg))
		}
		fmt.Printf("No service token for agent %q.\n", want)
		return nil
	}
	delete(tokens, key)
	if err := servicetoken.Save(path, tokens); err != nil {
		return err
	}
	fmt.Printf("Revoked service token for agent %q. A running claw removes it automatically within a few seconds.\n", key)
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

// configuredAgent reports whether the config lists an agent with the id.
func configuredAgent(cfg *config.Config, id string) bool {
	for i := range cfg.Agents.List {
		if config.SameAgentID(cfg.Agents.List[i].ID, id) {
			return true
		}
	}
	return false
}

func agentIDs(cfg *config.Config) string {
	ids := make([]string, 0, len(cfg.Agents.List))
	for i := range cfg.Agents.List {
		ids = append(ids, cfg.Agents.List[i].ID)
	}
	return strings.Join(ids, ", ")
}
