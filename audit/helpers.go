// ClawEh
// License: MIT

package audit

import (
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/PivotLLM/ClawEh/config"
	"github.com/PivotLLM/ClawEh/routing"
)

const (
	unknown  = "(unknown)"
	none     = "(none)"
	disabled = "(disabled)"
)

// dataDir is the base data directory every store path is derived from: the
// caller's Environment when it names one, otherwise what the config was loaded
// with.
func dataDir(cfg *config.Config, env Environment) string {
	if d := strings.TrimSpace(env.DataDir); d != "" {
		return d
	}
	return cfg.DataDir()
}

func onOff(b bool) string {
	if b {
		return "on"
	}
	return "off"
}

func yesNo(b bool) string {
	if b {
		return "yes"
	}
	return "no"
}

// setOrNot reports whether a credential is present, never its value.
func setOrNot(secret string) string {
	if strings.TrimSpace(secret) != "" {
		return "set"
	}
	return "not set"
}

func orValue(s, fallback string) string {
	if strings.TrimSpace(s) == "" {
		return fallback
	}
	return s
}

func joinOr(items []string, fallback string) string {
	if len(items) == 0 {
		return fallback
	}
	return strings.Join(items, ", ")
}

func itoa(n int) string { return strconv.Itoa(n) }

// pairs builds a two-column Item/Value table.
func pairs(caption string, rows ...[]string) Table {
	return Table{Caption: caption, Columns: []string{"Item", "Value"}, Rows: rows}
}

func row(cells ...string) []string { return cells }

// sortedKeys returns the keys of a string map in order, for stable output.
func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// redactURL strips any user:password from a proxy or endpoint URL so an
// authenticated proxy never leaks its credentials into the report.
func redactURL(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	u, err := url.Parse(raw)
	if err != nil || u.User == nil {
		return raw
	}
	u.User = nil
	return u.String() + " (credentials redacted)"
}

// isLoopback reports whether a bind host reaches only this machine. An empty
// host binds every interface.
func isLoopback(host string) bool {
	switch strings.TrimSpace(host) {
	case "127.0.0.1", "localhost", "::1", "[::1]":
		return true
	default:
		return false
	}
}

// bindAddr renders a listener's bind address the way the process binds it.
func bindAddr(host string, port int) string {
	if strings.TrimSpace(host) == "" {
		return "(all interfaces):" + itoa(port)
	}
	return host + ":" + itoa(port)
}

// expandHome mirrors config's ~ expansion for per-agent workspace paths.
func expandHome(path string) string {
	if path == "" || path[0] != '~' {
		return path
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return path
	}
	if len(path) > 1 && path[1] == '/' {
		return home + path[1:]
	}
	return home
}

// resolveWorkspace is the per-agent workspace the runtime uses
// (agent/instance.go resolveAgentWorkspace): an explicit workspace wins,
// otherwise <base_dir>/<id> with the routing-default id at <base_dir>/default.
func resolveWorkspace(cfg *config.Config, a *config.AgentConfig) string {
	if a != nil && strings.TrimSpace(a.Workspace) != "" {
		return expandHome(strings.TrimSpace(a.Workspace))
	}
	id := "default"
	if a != nil {
		if nid := routing.NormalizeAgentID(a.ID); nid != "" && nid != routing.DefaultAgentID {
			id = nid
		}
	}
	return filepath.Join(cfg.BaseDir(), id)
}

// resolvePath shows a path as the filesystem sees it: absolute with symlinks
// resolved. When that differs from the configured form, both are shown as
// "resolved [configured]"; a path that does not exist is shown as configured
// with a note saying so.
func resolvePath(configured string) (shown, note string) {
	abs, err := filepath.Abs(configured)
	if err != nil {
		return configured, "cannot resolve: " + err.Error()
	}
	resolved, err := filepath.EvalSymlinks(abs)
	if err != nil {
		if os.IsNotExist(err) {
			return configured, "does not exist"
		}
		return configured, "cannot resolve: " + err.Error()
	}
	if resolved != configured {
		return resolved + " [" + configured + "]", ""
	}
	return resolved, ""
}

// enabledAgents returns the agents the runtime would start.
func enabledAgents(cfg *config.Config) []*config.AgentConfig {
	out := make([]*config.AgentConfig, 0, len(cfg.Agents.List))
	for i := range cfg.Agents.List {
		if cfg.Agents.List[i].IsEnabled() {
			out = append(out, &cfg.Agents.List[i])
		}
	}
	return out
}

func agentIDs(agents []*config.AgentConfig) []string {
	ids := make([]string, 0, len(agents))
	for _, a := range agents {
		ids = append(ids, a.ID)
	}
	return ids
}

// defaultAgentID is the agent that receives traffic no binding claims
// (routing.RouteResolver.resolveDefaultAgentID).
func defaultAgentID(cfg *config.Config) string {
	agents := cfg.Agents.List
	if len(agents) == 0 {
		return routing.DefaultAgentID
	}
	for _, a := range agents {
		if a.Default && strings.TrimSpace(a.ID) != "" {
			return routing.NormalizeAgentID(a.ID)
		}
	}
	if id := strings.TrimSpace(agents[0].ID); id != "" {
		return routing.NormalizeAgentID(id)
	}
	return routing.DefaultAgentID
}

// effectiveTools is the agent's internal-tool allowlist as IsToolAllowed
// applies it: nil (key absent) means the install defaults, an empty list means
// no tools.
func effectiveTools(a *config.AgentConfig) []string {
	if a.Tools == nil {
		return config.DefaultAgentTools
	}
	return a.Tools
}

func agentHasTool(a *config.AgentConfig, name string) bool {
	return config.MatchToolPattern(effectiveTools(a), name)
}

// mcpEntryReach describes which tools on server an mcp_tools entry admits,
// under AgentConfig.MCPToolAllowed's rule (lowercase, underscore runs
// collapsed, entry is a prefix of "<server>_<tool>"). ok is false when the
// entry cannot match any tool on that server.
func mcpEntryReach(entry, server string) (desc string, ok bool) {
	e := collapseUnderscores(strings.ToLower(strings.TrimSpace(entry)))
	s := collapseUnderscores(strings.ToLower(strings.TrimSpace(server))) + "_"
	switch {
	case e == "":
		return "", false
	case strings.HasPrefix(s, e):
		return "all tools", true
	case strings.HasPrefix(e, s):
		return "tools starting with " + strings.TrimPrefix(e, s), true
	default:
		return "", false
	}
}

func collapseUnderscores(s string) string {
	for strings.Contains(s, "__") {
		s = strings.ReplaceAll(s, "__", "_")
	}
	return s
}

// agentsReachingServer lists the enabled agents whose mcp_tools admit at
// least one tool on the named MCP server.
func agentsReachingServer(cfg *config.Config, server string) []string {
	var ids []string
	for _, a := range enabledAgents(cfg) {
		for _, e := range a.MCPTools {
			if _, ok := mcpEntryReach(e, server); ok {
				ids = append(ids, a.ID)
				break
			}
		}
	}
	return ids
}
