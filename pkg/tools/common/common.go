// ClawEh
// License: MIT
//
// Copyright (c) 2026 Tenebris Technologies Inc.

package common

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/PivotLLM/ClawEh/pkg/global"
)

// errResult builds an error Result carrying err both as the LLM-facing text and
// the internal Err field.
func errResult(format string, args ...any) *global.Result {
	msg := fmt.Sprintf(format, args...)
	return &global.Result{ForLLM: msg, IsError: true, Err: fmt.Errorf("%s", msg)}
}

// confine cleans rel and joins it under base, verifying the result stays within
// base (rejecting "../" escapes and absolute paths). It returns the absolute
// resolved path.
func confine(base, rel string) (string, error) {
	if base == "" {
		return "", fmt.Errorf("directory is not configured")
	}
	rel = strings.TrimSpace(rel)
	if rel == "" {
		return "", fmt.Errorf("path is required")
	}
	cleaned := filepath.Clean(rel)
	if filepath.IsAbs(cleaned) {
		return "", fmt.Errorf("absolute paths are not allowed: %q", rel)
	}
	abs := filepath.Join(base, cleaned)
	// Verify abs is base or a descendant of base.
	relCheck, err := filepath.Rel(base, abs)
	if err != nil || relCheck == ".." || strings.HasPrefix(relCheck, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("path escapes the allowed directory: %q", rel)
	}
	return abs, nil
}

// strArg extracts a string argument; ok is false when absent or empty.
func strArg(args map[string]any, key string) (string, bool) {
	v, present := args[key]
	if !present {
		return "", false
	}
	s, _ := v.(string)
	s = strings.TrimSpace(s)
	return s, s != ""
}

// copyFile copies src to dst, creating intermediate directories for dst.
func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()

	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer out.Close()

	if _, err := io.Copy(out, in); err != nil {
		return err
	}
	return out.Close()
}

// listCommon lists files (name + size) under commonDir[/subdir].
func listCommon(commonDir string, args map[string]any) *global.Result {
	dir := commonDir
	if subdir, ok := strArg(args, "subdir"); ok {
		abs, err := confine(commonDir, subdir)
		if err != nil {
			return errResult("common_list: %v", err)
		}
		dir = abs
	}
	if commonDir == "" {
		return errResult("common_list: common directory is not configured")
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return &global.Result{ForLLM: "(empty)"}
		}
		return errResult("common_list: %v", err)
	}

	var b strings.Builder
	count := 0
	for _, e := range entries {
		info, err := e.Info()
		if err != nil {
			continue
		}
		if e.IsDir() {
			fmt.Fprintf(&b, "%s/\n", e.Name())
		} else {
			fmt.Fprintf(&b, "%s\t%d bytes\n", e.Name(), info.Size())
		}
		count++
	}
	if count == 0 {
		return &global.Result{ForLLM: "(empty)"}
	}
	return &global.Result{ForLLM: strings.TrimRight(b.String(), "\n")}
}

// getCommon copies commonDir/<name> into <workspace>/files/<as|basename(name)>.
func getCommon(commonDir, workspace string, args map[string]any) *global.Result {
	name, ok := strArg(args, "name")
	if !ok {
		return errResult("common_get: 'name' is required")
	}
	if workspace == "" {
		return errResult("common_get: workspace is not configured")
	}
	srcAbs, err := confine(commonDir, name)
	if err != nil {
		return errResult("common_get: %v", err)
	}

	dstRel := filepath.Base(name)
	if as, ok := strArg(args, "as"); ok {
		dstRel = as
	}
	// Writes into the agent workspace land under files/, matching the
	// read-only-workspace default.
	filesRoot := filepath.Join(workspace, "files")
	dstAbs, err := confine(filesRoot, dstRel)
	if err != nil {
		return errResult("common_get: %v", err)
	}

	if err := copyFile(srcAbs, dstAbs); err != nil {
		return errResult("common_get: copy failed: %v", err)
	}
	rel, _ := filepath.Rel(workspace, dstAbs)
	return &global.Result{ForLLM: fmt.Sprintf("Copied %q to workspace %s", name, rel)}
}

// putCommon copies <workspace>/<path> into commonDir/<as|basename(path)>.
func putCommon(commonDir, workspace string, args map[string]any) *global.Result {
	path, ok := strArg(args, "path")
	if !ok {
		return errResult("common_put: 'path' is required")
	}
	if workspace == "" {
		return errResult("common_put: workspace is not configured")
	}
	if commonDir == "" {
		return errResult("common_put: common directory is not configured")
	}
	// Reads are allowed workspace-wide.
	srcAbs, err := confine(workspace, path)
	if err != nil {
		return errResult("common_put: %v", err)
	}

	dstRel := filepath.Base(path)
	if as, ok := strArg(args, "as"); ok {
		dstRel = as
	}
	dstAbs, err := confine(commonDir, dstRel)
	if err != nil {
		return errResult("common_put: %v", err)
	}

	// Create the common directory on first write.
	if err := os.MkdirAll(commonDir, 0o755); err != nil {
		return errResult("common_put: %v", err)
	}
	if err := copyFile(srcAbs, dstAbs); err != nil {
		return errResult("common_put: copy failed: %v", err)
	}
	return &global.Result{ForLLM: fmt.Sprintf("Copied workspace %q to common %s", path, dstRel)}
}

// deleteCommon removes commonDir/<name>.
func deleteCommon(commonDir string, args map[string]any) *global.Result {
	name, ok := strArg(args, "name")
	if !ok {
		return errResult("common_delete: 'name' is required")
	}
	abs, err := confine(commonDir, name)
	if err != nil {
		return errResult("common_delete: %v", err)
	}
	if err := os.Remove(abs); err != nil {
		return errResult("common_delete: %v", err)
	}
	return &global.Result{ForLLM: fmt.Sprintf("Deleted %q from common", name)}
}
