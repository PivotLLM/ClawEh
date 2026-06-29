package api

import (
	"context"
	"encoding/json"
	"net/http"
	"os/exec"
	"strings"
	"time"
)

// knownCLIs maps each CLI provider protocol to its default binary, so the setup
// wizard can show which CLI agents are actually installed (rather than letting a
// user configure a CLI whose binary isn't on PATH).
var knownCLIs = []struct {
	Protocol string
	Label    string
	Binary   string
}{
	{"claude-cli", "Claude Code", "claude"},
	{"codex-cli", "Codex", "codex"},
	{"gemini-cli", "Gemini CLI", "gemini"},
}

type cliInfo struct {
	Protocol  string `json:"protocol"`
	Label     string `json:"label"`
	Binary    string `json:"binary"`
	Installed bool   `json:"installed"`
	Path      string `json:"path,omitempty"`
	Version   string `json:"version,omitempty"`
}

func (h *Handler) registerSystemCLIRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/system/clis", h.handleListCLIs)
}

// handleListCLIs reports which known CLI agents are installed on the host, with
// their resolved path and (best-effort) version.
func (h *Handler) handleListCLIs(w http.ResponseWriter, r *http.Request) {
	out := make([]cliInfo, 0, len(knownCLIs))
	for _, c := range knownCLIs {
		info := cliInfo{Protocol: c.Protocol, Label: c.Label, Binary: c.Binary}
		if p, err := exec.LookPath(c.Binary); err == nil {
			info.Installed = true
			info.Path = p
			info.Version = cliVersion(c.Binary)
		}
		out = append(out, info)
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(out)
}

// cliVersion runs "<bin> --version" with a short timeout and returns the first
// line, best-effort (empty string on any error/timeout).
func cliVersion(bin string) string {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, bin, "--version").Output()
	if err != nil {
		return ""
	}
	line := strings.TrimSpace(string(out))
	if i := strings.IndexByte(line, '\n'); i >= 0 {
		line = strings.TrimSpace(line[:i])
	}
	return line
}
