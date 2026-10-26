// Package workspace manages agent workspace initialization.
package workspace

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/PivotLLM/ClawEh/config"
	"github.com/PivotLLM/ClawEh/logger"
	"github.com/PivotLLM/ClawEh/templates"
)

// Populate writes the default workspace template files into workspace, skipping
// any file that already exists. It is safe to call on every startup — existing
// agent customizations are never overwritten.
//
// COMPRESSION.md and MEMORY.md are user-deletable: they are seeded only into a
// brand-new (uninitialized) workspace, so deleting one sticks and a personalized
// agent is not disturbed on restart.
//
// The channel prompt files (templates/channels/<KIND>.md, seeded as
// <workspace>/<KIND>.md) are seeded only for the given channel kinds, the ones
// in use (config.Config.ChannelKindsInUse); a kind's file that exists is never
// touched, whether its kind is in use or not. Before seeding, an old
// channel-<channel>.md is renamed to its kind's file (migrateChannelFiles),
// with kindOf resolving a channel name to its kind (config.Config.ChannelKind).
// A nil kindOf skips that migration.
//
// Directories are created with dirMode and files with fileMode (the agents
// area's modes, security.permissions.agents).
func Populate(workspace string, dirMode, fileMode os.FileMode, kinds []string, kindOf func(channel string) string) {
	if err := os.MkdirAll(workspace, dirMode); err != nil {
		logger.WarnCF("workspace", "Failed to create workspace directory",
			map[string]any{"dir": workspace, "error": err.Error()})
		return
	}

	// The agent's writable area is <workspace>/files (the read-only-workspace
	// default; see AgentDefaults.WorkspaceWriteSubdir). Ensure it always exists
	// so the agent has somewhere to write from first run.
	if err := os.MkdirAll(filepath.Join(workspace, "files"), dirMode); err != nil {
		logger.WarnCF("workspace", "Failed to create workspace files directory",
			map[string]any{"dir": filepath.Join(workspace, "files"), "error": err.Error()})
	}

	// A workspace is "initialized" once its core identity file exists. On an
	// initialized workspace we must not recreate user-deletable files, or a
	// deleted COMPRESSION/MEMORY file would silently reappear.
	initialized := fileExists(filepath.Join(workspace, "AGENTS.md"))

	// Seeded only into a brand-new workspace (so deleting one sticks).
	seedOnce := map[string]bool{
		"COMPRESSION.md": true,
		"MEMORY.md":      true,
	}

	migrateChannelFiles(workspace, kindOf)

	err := fs.WalkDir(templates.FS, ".", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if path == channelTemplatesDir {
				return fs.SkipDir // seeded per kind in use, below
			}
			return os.MkdirAll(filepath.Join(workspace, path), dirMode)
		}
		if seedOnce[path] && initialized {
			return nil
		}
		dest := filepath.Join(workspace, path)
		if _, statErr := os.Stat(dest); statErr == nil {
			return nil // already exists — preserve user customisation
		}
		data, err := templates.FS.ReadFile(path)
		if err != nil {
			return err
		}
		if err := os.MkdirAll(filepath.Dir(dest), dirMode); err != nil {
			return err
		}
		return os.WriteFile(dest, data, fileMode)
	})
	if err != nil {
		logger.WarnCF("workspace", "Failed to populate workspace templates",
			map[string]any{"dir": workspace, "error": err.Error()})
	}

	seedChannelFiles(workspace, fileMode, kinds)
}

// channelTemplatesDir holds one prompt template per channel kind, named as the
// workspace file it seeds (config.ChannelPromptFile).
const channelTemplatesDir = "channels"

// seedChannelFiles writes the prompt file of each channel kind in kinds that
// the workspace does not have yet.
func seedChannelFiles(workspace string, fileMode os.FileMode, kinds []string) {
	for _, kind := range kinds {
		name := config.ChannelPromptFile(kind)
		if name == "" {
			continue
		}
		dest := filepath.Join(workspace, name)
		if fileExists(dest) {
			continue // preserve user customisation
		}
		data, err := templates.FS.ReadFile(channelTemplatesDir + "/" + name)
		if err != nil {
			logger.WarnCF("workspace", "No channel prompt template",
				map[string]any{"dir": workspace, "kind": kind, "error": err.Error()})
			continue
		}
		if err := os.WriteFile(dest, data, fileMode); err != nil {
			logger.WarnCF("workspace", "Failed to write channel prompt file",
				map[string]any{"dir": workspace, "file": name, "error": err.Error()})
		}
	}
}

// oldChannelFilePrefix and oldChannelFileSuffix frame the per-channel prompt
// file name read before the per-kind <KIND>.md files: channel-<channel>.md.
const (
	oldChannelFilePrefix = "channel-"
	oldChannelFileSuffix = ".md"
)

// migrateChannelFiles renames each channel-<channel>.md whose channel has a
// known kind to that kind's file, in file-name order, so the first of several
// wins. When the kind's file already exists (a second Telegram bot's file,
// say), the old file is left in place and a warning says it is no longer
// read; so is a file whose channel has no known kind.
func migrateChannelFiles(workspace string, kindOf func(channel string) string) {
	if kindOf == nil {
		return
	}
	entries, err := os.ReadDir(workspace)
	if err != nil {
		return
	}
	for _, e := range entries {
		old := e.Name()
		if !e.Type().IsRegular() || !strings.HasPrefix(old, oldChannelFilePrefix) || !strings.HasSuffix(old, oldChannelFileSuffix) {
			continue
		}
		channel := strings.TrimSuffix(strings.TrimPrefix(old, oldChannelFilePrefix), oldChannelFileSuffix)
		name := config.ChannelPromptFile(kindOf(channel))
		if name == "" {
			logger.WarnCF("workspace", "Old channel prompt file is no longer read; it names no channel",
				map[string]any{"dir": workspace, "old": old})
			continue
		}
		fields := map[string]any{"dir": workspace, "old": old, "new": name}
		dest := filepath.Join(workspace, name)
		if fileExists(dest) {
			logger.WarnCF("workspace", "Old channel prompt file is no longer read; "+name+" is used instead", fields)
			continue
		}
		if err := os.Rename(filepath.Join(workspace, old), dest); err != nil {
			fields["error"] = err.Error()
			logger.WarnCF("workspace", "Failed to rename old channel prompt file", fields)
			continue
		}
		logger.InfoCF("workspace", "Renamed channel prompt file", fields)
	}
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}
