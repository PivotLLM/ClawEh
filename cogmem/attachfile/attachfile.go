// ClawEh - Cognitive Memory
// License: MIT

// Package attachfile resolves the markdown files that cognitive memories point
// at. It is the seam between cogmem (which never touches the filesystem) and the
// file tools' permission stack: every read goes through files.Reader, so a
// memory can only attach a document the agent is already allowed to read —
// files/, an external mount such as maestro/, or an allow-listed host path.
//
// It is imported by the agent wiring (per-turn loading) and by the cogmem tools
// (create-time validation) so both apply one identical rule.
package attachfile

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/PivotLLM/ClawEh/cogmem"
	"github.com/PivotLLM/ClawEh/config"
	"github.com/PivotLLM/ClawEh/logger"
	"github.com/PivotLLM/ClawEh/tools/files"
)

// markdownExts are the extensions a memory may point at. Attachments are
// injected verbatim into the prompt, so the format has to be one the model reads
// as text — this is a deliberate allowlist, not a sniff.
var markdownExts = map[string]bool{".md": true, ".markdown": true}

// ErrNotMarkdown describes a reference with the wrong extension.
func errNotMarkdown(ref string) error {
	return fmt.Errorf("%q is not a markdown file (expected a .md or .markdown path)", ref)
}

// Check validates that ref is a markdown file the agent may read, returning its
// size in bytes. Used at memory-create time so a bad pointer is rejected while
// the user is still in the conversation, rather than silently failing later in
// every prompt.
func Check(cfg *config.Config, workspace, ref string) (int64, error) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return 0, fmt.Errorf("file reference is empty")
	}
	if !markdownExts[strings.ToLower(filepath.Ext(ref))] {
		return 0, errNotMarkdown(ref)
	}
	if workspace == "" {
		return 0, fmt.Errorf("no workspace configured")
	}
	info, err := files.NewReader(cfg, workspace).Stat(ref)
	if err != nil {
		return 0, fmt.Errorf("cannot read %s: %w", ref, err)
	}
	if info.IsDir() {
		return 0, fmt.Errorf("%s is a directory, not a markdown file", ref)
	}
	return info.Size(), nil
}

// NewLoader returns the per-turn attachment loader for an agent. Failures are
// returned (cogmem renders them as an explicit "not included" note) and logged,
// so a moved or newly-unreadable file is visible in the log rather than quietly
// dropping context.
func NewLoader(cfg *config.Config, agentID, workspace string) cogmem.AttachmentLoader {
	return func(ref string, maxBytes int) (cogmem.Attachment, error) {
		size, err := Check(cfg, workspace, ref)
		if err != nil {
			logger.WarnCF("cogmem", "attached document unavailable", map[string]any{
				"agent_id": agentID, "ref": ref, "error": err.Error(),
			})
			return cogmem.Attachment{}, err
		}

		data, more, err := files.NewReader(cfg, workspace).ReadFileLimit(ref, maxBytes)
		if err != nil {
			logger.WarnCF("cogmem", "attached document read failed", map[string]any{
				"agent_id": agentID, "ref": ref, "error": err.Error(),
			})
			return cogmem.Attachment{}, fmt.Errorf("cannot read %s: %w", ref, err)
		}

		att := cogmem.Attachment{Ref: ref, Content: string(data), Size: size}
		if more {
			att.Content = truncateAtLine(att.Content)
			att.Truncated = true
			logger.WarnCF("cogmem", "attached document truncated", map[string]any{
				"agent_id": agentID, "ref": ref,
				"size": size, "included": len(att.Content), "limit": maxBytes,
			})
		} else {
			logger.DebugCF("cogmem", "attached document loaded", map[string]any{
				"agent_id": agentID, "ref": ref, "bytes": len(att.Content),
			})
		}
		return att, nil
	}
}

// truncateAtLine cuts s back to its last complete line so the model is not
// handed a sentence that stops mid-word. A single line longer than the whole cap
// is returned as-is.
func truncateAtLine(s string) string {
	if i := strings.LastIndexByte(s, '\n'); i > 0 {
		return s[:i]
	}
	return s
}
