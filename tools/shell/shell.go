package shell

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"time"

	"github.com/PivotLLM/ClawEh/config"
	"github.com/PivotLLM/ClawEh/internal/childenv"
	"github.com/PivotLLM/ClawEh/logger"
	"github.com/PivotLLM/ClawEh/tools"
)

type ExecTool struct {
	workingDir          string
	timeout             time.Duration
	denyPatterns        []*regexp.Regexp
	allowPatterns       []*regexp.Regexp
	customAllowPatterns []*regexp.Regexp
}

var defaultDenyPatterns = []*regexp.Regexp{
	regexp.MustCompile(`\brm\s+-[rf]{1,2}\b`),
	regexp.MustCompile(`\bdel\s+/[fq]\b`),
	regexp.MustCompile(`\brmdir\s+/s\b`),
	// Match disk wiping commands (must be followed by space/args)
	regexp.MustCompile(
		`\b(format|mkfs|diskpart)\b\s`,
	),
	regexp.MustCompile(`\bdd\s+if=`),
	// Block writes to block devices (all common naming schemes).
	regexp.MustCompile(
		`>\s*/dev/(sd[a-z]|hd[a-z]|vd[a-z]|xvd[a-z]|nvme\d|mmcblk\d|loop\d|dm-\d|md\d|sr\d|nbd\d)`,
	),
	regexp.MustCompile(`\b(shutdown|reboot|poweroff)\b`),
	regexp.MustCompile(`:\(\)\s*\{.*\};\s*:`),
	regexp.MustCompile(`\$\([^)]+\)`),
	regexp.MustCompile(`\$\{[^}]+\}`),
	regexp.MustCompile("`[^`]+`"),
	regexp.MustCompile(`\|\s*sh\b`),
	regexp.MustCompile(`\|\s*bash\b`),
	regexp.MustCompile(`;\s*rm\s+-[rf]`),
	regexp.MustCompile(`&&\s*rm\s+-[rf]`),
	regexp.MustCompile(`\|\|\s*rm\s+-[rf]`),
	regexp.MustCompile(`<<\s*EOF`),
	regexp.MustCompile(`\$\(\s*cat\s+`),
	regexp.MustCompile(`\$\(\s*curl\s+`),
	regexp.MustCompile(`\$\(\s*wget\s+`),
	regexp.MustCompile(`\$\(\s*which\s+`),
	regexp.MustCompile(`\bsudo\b`),
	regexp.MustCompile(`\bchmod\s+[0-7]{3,4}\b`),
	regexp.MustCompile(`\bchown\b`),
	regexp.MustCompile(`\bpkill\b`),
	regexp.MustCompile(`\bkillall\b`),
	regexp.MustCompile(`\bkill\b`),
	regexp.MustCompile(`\bcurl\b.*\|\s*(sh|bash)`),
	regexp.MustCompile(`\bwget\b.*\|\s*(sh|bash)`),
	regexp.MustCompile(`\bnpm\s+install\s+-g\b`),
	regexp.MustCompile(`\bpip\s+install\s+--user\b`),
	regexp.MustCompile(`\bapt\s+(install|remove|purge)\b`),
	regexp.MustCompile(`\byum\s+(install|remove)\b`),
	regexp.MustCompile(`\bdnf\s+(install|remove)\b`),
	regexp.MustCompile(`\bdocker\s+run\b`),
	regexp.MustCompile(`\bdocker\s+exec\b`),
	regexp.MustCompile(`\bgit\s+push\b`),
	regexp.MustCompile(`\bgit\s+force\b`),
	regexp.MustCompile(`\bssh\b.*@`),
	regexp.MustCompile(`\beval\b`),
	regexp.MustCompile(`\bsource\s+.*\.sh\b`),
}

// NewExecTool builds the shell tool with the default deny patterns. Commands
// run in workingDir (the agent workspace) unless a call names another
// working_dir; nothing confines them to it.
func NewExecTool(workingDir string) (*ExecTool, error) {
	return NewExecToolWithConfig(workingDir, nil)
}

// NewExecToolWithConfig is NewExecTool with the deny, allow and timeout
// settings of config (tools.exec).
func NewExecToolWithConfig(workingDir string, config *config.Config) (*ExecTool, error) {
	denyPatterns := make([]*regexp.Regexp, 0)
	customAllowPatterns := make([]*regexp.Regexp, 0)

	if config != nil {
		execConfig := config.Tools.Exec
		enableDenyPatterns := execConfig.EnableDenyPatterns
		if enableDenyPatterns {
			denyPatterns = append(denyPatterns, defaultDenyPatterns...)
			if len(execConfig.CustomDenyPatterns) > 0 {
				logger.InfoCF("shell", "Using custom deny patterns", map[string]any{"patterns": execConfig.CustomDenyPatterns})
				for _, pattern := range execConfig.CustomDenyPatterns {
					re, err := regexp.Compile(pattern)
					if err != nil {
						return nil, fmt.Errorf("invalid custom deny pattern %q: %w", pattern, err)
					}
					denyPatterns = append(denyPatterns, re)
				}
			}
		} else {
			// If deny patterns are disabled, we won't add any patterns, allowing all commands.
			logger.WarnC("shell", "Deny patterns are disabled - all commands will be allowed")
		}
		for _, pattern := range execConfig.CustomAllowPatterns {
			re, err := regexp.Compile(pattern)
			if err != nil {
				return nil, fmt.Errorf("invalid custom allow pattern %q: %w", pattern, err)
			}
			customAllowPatterns = append(customAllowPatterns, re)
		}
	} else {
		denyPatterns = append(denyPatterns, defaultDenyPatterns...)
	}

	timeout := 60 * time.Second
	if config != nil && config.Tools.Exec.TimeoutSeconds > 0 {
		timeout = time.Duration(config.Tools.Exec.TimeoutSeconds) * time.Second
	}

	return &ExecTool{
		workingDir:          workingDir,
		timeout:             timeout,
		denyPatterns:        denyPatterns,
		allowPatterns:       nil,
		customAllowPatterns: customAllowPatterns,
	}, nil
}

func (t *ExecTool) Name() string {
	return "shell_exec"
}

func (t *ExecTool) Description() string {
	return "Run git, builds, tests and other programs: executes a shell command and returns its output. " +
		"Not for finding or reading files: use file_glob, file_search_lines and file_read_lines."
}

func (t *ExecTool) Parameters() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"command": map[string]any{
				"type":        "string",
				"description": "The shell command to execute",
			},
			"working_dir": map[string]any{
				"type":        "string",
				"description": "Optional working directory for the command (relative paths start at your workspace)",
			},
		},
		"required": []string{"command"},
	}
}

func (t *ExecTool) Execute(ctx context.Context, args map[string]any) *tools.ToolResult {
	command, ok := args["command"].(string)
	if !ok {
		return tools.ErrorResult("command is required")
	}

	cwd := t.workingDir
	if wd, ok := args["working_dir"].(string); ok && wd != "" {
		cwd = wd
		if !filepath.IsAbs(wd) && t.workingDir != "" {
			cwd = filepath.Join(t.workingDir, wd)
		}
	}

	if cwd == "" {
		wd, err := os.Getwd()
		if err == nil {
			cwd = wd
		}
	}

	if guardError := t.guardCommand(command); guardError != "" {
		return guardRefusal(guardError)
	}

	// timeout == 0 means no timeout
	var cmdCtx context.Context
	var cancel context.CancelFunc
	if t.timeout > 0 {
		cmdCtx, cancel = context.WithTimeout(ctx, t.timeout)
	} else {
		cmdCtx, cancel = context.WithCancel(ctx)
	}
	defer cancel()

	var cmd *exec.Cmd
	if runtime.GOOS == "windows" {
		cmd = exec.CommandContext(cmdCtx, "powershell", "-NoProfile", "-NonInteractive", "-Command", command) //nolint:gosec // user command by design; screened by denyPatterns (tools.exec.enable_deny_patterns) and allowPatterns
	} else {
		cmd = exec.CommandContext(cmdCtx, "sh", "-c", command) //nolint:gosec // user command by design; screened by denyPatterns (tools.exec.enable_deny_patterns) and allowPatterns
	}
	if cwd != "" {
		cmd.Dir = cwd
	}
	// The child gets an allowlisted environment, not the service's: CLAW_* and
	// ALERTER_* carry tokens and credentials that a shell command must not read.
	cmd.Env = childenv.Base()

	prepareCommandForTermination(cmd)

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Start(); err != nil {
		return tools.ErrorResult(fmt.Sprintf("failed to start command: %v", err))
	}

	done := make(chan error, 1)
	go func() {
		done <- cmd.Wait()
	}()

	var err error
	select {
	case err = <-done:
	case <-cmdCtx.Done():
		if termErr := terminateProcessTree(cmd); termErr != nil {
			logger.DebugCF("shell", "process tree termination reported an error", map[string]any{"error": termErr.Error()})
		}
		select {
		case err = <-done:
		case <-time.After(2 * time.Second):
			if cmd.Process != nil {
				if killErr := cmd.Process.Kill(); killErr != nil {
					logger.DebugCF("shell", "process kill reported an error", map[string]any{"error": killErr.Error()})
				}
			}
			err = <-done
		}
	}

	output := stdout.String()
	if stderr.Len() > 0 {
		output += "\nSTDERR:\n" + stderr.String()
	}

	if err != nil {
		if errors.Is(cmdCtx.Err(), context.DeadlineExceeded) {
			msg := fmt.Sprintf("Command timed out after %v", t.timeout)
			return &tools.ToolResult{
				ForLLM:  msg,
				ForUser: msg,
				IsError: true,
			}
		}
		output += fmt.Sprintf("\nExit code: %v", err)
	}

	if output == "" {
		output = "(no output)"
	}

	maxLen := 10000
	if len(output) > maxLen {
		output = output[:maxLen] + fmt.Sprintf("\n... (truncated, %d more chars)", len(output)-maxLen)
	}

	if err != nil {
		return &tools.ToolResult{
			ForLLM:  output,
			ForUser: output,
			IsError: true,
		}
	}

	return &tools.ToolResult{
		ForLLM:  output,
		ForUser: output,
		IsError: false,
	}
}

// guardCommand screens a command against the deny and allow patterns. It
// does not look at the paths a command names: the shell runs with the app
// account's full access.
func (t *ExecTool) guardCommand(command string) string {
	cmd := strings.TrimSpace(command)
	lower := strings.ToLower(cmd)

	// Custom allow patterns exempt a command from deny checks.
	explicitlyAllowed := false
	for _, pattern := range t.customAllowPatterns {
		if pattern.MatchString(lower) {
			explicitlyAllowed = true
			break
		}
	}

	if !explicitlyAllowed {
		for _, pattern := range t.denyPatterns {
			if pattern.MatchString(lower) {
				return "Command blocked by safety guard (dangerous pattern detected)"
			}
		}
	}

	if len(t.allowPatterns) > 0 {
		allowed := false
		for _, pattern := range t.allowPatterns {
			if pattern.MatchString(lower) {
				allowed = true
				break
			}
		}
		if !allowed {
			return "Command blocked by safety guard (not in allowlist)"
		}
	}

	return ""
}

func (t *ExecTool) SetTimeout(timeout time.Duration) {
	t.timeout = timeout
}

func (t *ExecTool) SetAllowPatterns(patterns []string) error {
	t.allowPatterns = make([]*regexp.Regexp, 0, len(patterns))
	for _, p := range patterns {
		re, err := regexp.Compile(p)
		if err != nil {
			return fmt.Errorf("invalid allow pattern %q: %w", p, err)
		}
		t.allowPatterns = append(t.allowPatterns, re)
	}
	return nil
}

// guardRefusal is the result for a command the safety guard blocked: an
// expected refusal (logged as a warning), not a failure.
func guardRefusal(msg string) *tools.ToolResult {
	return tools.ErrorResult(msg).WithError(tools.Refusal(errors.New(msg)))
}
