// ClawEh
// License: MIT

package maestro

import (
	"bytes"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	mconfig "github.com/PivotLLM/Maestro/config"

	"github.com/PivotLLM/ClawEh/config"
	"github.com/PivotLLM/ClawEh/logger"
	"github.com/PivotLLM/ClawEh/tools/files"
)

// runnerConfig maps the agent's maestro block onto Maestro's runner settings.
// Unset values are left at zero so Maestro applies its own defaults.
func runnerConfig(mc *config.MaestroConfig) mconfig.Runner {
	if mc == nil {
		return mconfig.Runner{}
	}
	return mconfig.Runner{
		MaxConcurrent: mc.MaxConcurrent,
		RateLimit:     mconfig.RateLimit{MaxRequests: mc.RateLimitRequests, PeriodSeconds: mc.RateLimitPeriod},
		AllowParallel: mc.AllowParallel,
	}
}

// referenceDirsFromAllowedFolders maps the agent's allowed folders onto
// Maestro reference directories (read-only inside Maestro, whatever the file
// permissions say). Each is named after its last path component reduced to
// one word Maestro accepts (referenceName); a name already taken, in any case,
// gets "-2", "-3", … in config order. Names are given to every configured
// folder before any is skipped, so a folder that goes missing does not rename
// the others. A folder that is not an existing directory is skipped, so
// Maestro never creates a directory on the host's behalf.
func referenceDirsFromAllowedFolders(agentCfg *config.AgentConfig) []mconfig.ReferenceDir {
	var (
		out   []mconfig.ReferenceDir
		taken = map[string]bool{}
	)
	for _, p := range agentCfg.AllowedFolderPaths() {
		name := referenceName(p)
		unique := name
		for n := 2; taken[strings.ToLower(unique)]; n++ {
			unique = name + "-" + strconv.Itoa(n)
		}
		taken[strings.ToLower(unique)] = true
		if st, err := os.Stat(p); err != nil || !st.IsDir() {
			continue
		}
		out = append(out, mconfig.ReferenceDir{Path: p, Mount: unique})
	}
	return out
}

// referenceNameRe matches the runs of characters a reference name leaves out.
var referenceNameRe = regexp.MustCompile(`[^A-Za-z0-9_-]+`)

// referenceName is the Maestro reference name for a folder: its last path
// component with every run of characters other than letters, digits, '_' and
// '-' turned into one '-', and leading and trailing '-' removed, so it is a
// single word with no path separator and never "." or "..". A folder whose
// name has nothing left ("/", "...") is "folder".
func referenceName(p string) string {
	name := strings.Trim(referenceNameRe.ReplaceAllString(filepath.Base(p), "-"), "-")
	if name == "" {
		return "folder"
	}
	return name
}

// importAllowed returns the predicate Maestro consults before importing a host
// path: exactly what the agent's own file tools may read. When the agent is not
// confined to its workspace, that is everything; otherwise the workspace
// (which holds Maestro's own data), every allowed folder, and any path matching
// tools.allow_read_paths (plus the skills directory). Paths are compared after
// resolving symlinks on both sides.
func importAllowed(cfg *config.Config, agentCfg *config.AgentConfig, workspace string) func(string) bool {
	restrict, allow := files.ReadPolicy(cfg)
	var roots []string
	addRoot := func(p string) {
		p = strings.TrimSpace(p)
		if p == "" {
			return
		}
		if abs, err := filepath.Abs(p); err == nil {
			p = abs
		}
		if resolved, err := filepath.EvalSymlinks(p); err == nil {
			p = resolved
		}
		roots = append(roots, p)
	}
	addRoot(workspace)
	if agentCfg != nil {
		for _, p := range agentCfg.AllowedFolderPaths() {
			addRoot(p)
		}
	}
	return func(p string) bool {
		if !restrict {
			return true
		}
		for _, r := range roots {
			if p == r || strings.HasPrefix(p, r+string(os.PathSeparator)) {
				return true
			}
		}
		for _, re := range allow {
			if re.MatchString(p) {
				return true
			}
		}
		return false
	}
}

// maestroLine matches Maestro's log line format:
// "YYYY-MM-DD HH:MM:SS [LEVEL] [pid] message" (see Maestro logging.NewWithWriter).
var maestroLine = regexp.MustCompile(`^\d{4}-\d{2}-\d{2} \d{2}:\d{2}:\d{2} \[([A-Z]+)\] \[\d+\] (.*)$`)

// logWriter forwards Maestro's log lines to the central logger, component
// "maestro", tagged with the agent. It buffers partial writes until a newline.
type logWriter struct {
	agent string
	buf   bytes.Buffer
	// emit is the sink; nil means the central logger. Tests replace it.
	emit func(level, message string, fields map[string]any)
}

func (w *logWriter) Write(p []byte) (int, error) {
	w.buf.Write(p)
	for {
		i := bytes.IndexByte(w.buf.Bytes(), '\n')
		if i < 0 {
			return len(p), nil
		}
		line := string(w.buf.Next(i + 1))
		w.forward(strings.TrimRight(line, "\r\n"))
	}
}

func (w *logWriter) forward(line string) {
	if line == "" {
		return
	}
	level, msg := "INFO", line
	if m := maestroLine.FindStringSubmatch(line); m != nil {
		level, msg = m[1], m[2]
	}
	fields := map[string]any{"agent": w.agent}
	if w.emit != nil {
		w.emit(level, msg, fields)
		return
	}
	switch level {
	case "DEBUG":
		logger.DebugCF("maestro", msg, fields)
	case "WARN":
		logger.WarnCF("maestro", msg, fields)
	case "ERROR", "FATAL":
		logger.ErrorCF("maestro", msg, fields)
	default:
		logger.InfoCF("maestro", msg, fields)
	}
}
