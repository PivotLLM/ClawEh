// ClawEh
// License: MIT
//
// Copyright (c) 2026 Tenebris Technologies Inc.

package files

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"regexp"
	"unicode"
	"unicode/utf8"

	"github.com/PivotLLM/ClawEh/tools"
)

// CountFileTool reports a file's line, word and character counts — the same
// figures as wc(1), computed the same way.
//
// It streams rather than reading the file in, so counting is not bounded by the
// read-size limit the read tools enforce: asking how big a file is should not
// require being able to hold it, and "too large to read" is exactly when the
// answer is most useful.
type CountFileTool struct {
	sysFs fileSystem
}

// NewCountFileTool builds the counting tool. It shares the read policy of the
// other file tools — same workspace confinement, same allow-path patterns — so
// a file that cannot be read cannot be measured either.
func NewCountFileTool(workspace string, restrict bool, allowPaths ...[]*regexp.Regexp) *CountFileTool {
	var patterns []*regexp.Regexp
	if len(allowPaths) > 0 {
		patterns = allowPaths[0]
	}
	return &CountFileTool{sysFs: buildFs(workspace, restrict, patterns)}
}

func (t *CountFileTool) Name() string { return "file_count" }

func (t *CountFileTool) Description() string {
	return "Count the lines, words, characters and bytes in a file, like the Unix `wc` command. " +
		"Use this to size a file before reading it, or to check whether it changed. " +
		"Counts match wc exactly: `lines` is the number of newline characters, so a file whose last " +
		"line has no trailing newline reports one fewer than you might count by eye — `final_newline` " +
		"in the result tells you which case you are in. `characters` counts Unicode characters and " +
		"`bytes` counts bytes; they differ for non-ASCII text. The file is streamed, so this works on " +
		"files too large to read."
}

func (t *CountFileTool) Parameters() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"path": map[string]any{
				"type":        "string",
				"description": "Path to the file to count, relative to the workspace.",
			},
		},
		"required": []string{"path"},
	}
}

// fileCounts is the result shape. It is JSON so a caller can compare fields
// directly — including a cron watch job, for which a changed byte or line count
// is a cheap way to notice a file was touched.
type fileCounts struct {
	Path         string `json:"path"`
	Lines        int64  `json:"lines"`
	Words        int64  `json:"words"`
	Characters   int64  `json:"characters"`
	Bytes        int64  `json:"bytes"`
	FinalNewline bool   `json:"final_newline"`
	InvalidUTF8  bool   `json:"invalid_utf8,omitempty"`
}

func (t *CountFileTool) Execute(_ context.Context, args map[string]any) *tools.ToolResult {
	path, ok := args["path"].(string)
	if !ok || path == "" {
		return tools.ErrorResult("path is required")
	}

	f, err := t.sysFs.Open(path)
	if err != nil {
		return tools.ErrorResult(fmt.Sprintf("failed to open %q: %v", path, err))
	}
	defer f.Close()

	counts, err := countReader(bufio.NewReaderSize(f, 64*1024))
	if err != nil {
		return tools.ErrorResult(fmt.Sprintf("failed to read %q: %v", path, err))
	}
	counts.Path = path

	encoded, err := json.Marshal(counts)
	if err != nil {
		return tools.ErrorResult(fmt.Sprintf("failed to encode counts: %v", err))
	}
	return tools.SilentResult(string(encoded))
}

// countReader walks the stream once, counting as wc does.
//
// Words are runs of non-whitespace, so the count is transitions into a word
// rather than the number of separators — which is why runs of spaces, tabs and
// newlines between words do not inflate it.
func countReader(r io.RuneReader) (fileCounts, error) {
	var (
		c        fileCounts
		inWord   bool
		lastRune rune
		sawAny   bool
	)

	for {
		ch, size, err := r.ReadRune()
		if err == io.EOF {
			break
		}
		if err != nil {
			return c, err
		}

		sawAny = true
		lastRune = ch
		c.Bytes += int64(size)
		c.Characters++

		// ReadRune yields U+FFFD with size 1 for a byte that is not valid UTF-8.
		// Reported rather than rejected: a binary or mis-encoded file still has a
		// meaningful byte count, and the flag says not to trust the others.
		if ch == utf8.RuneError && size == 1 {
			c.InvalidUTF8 = true
		}

		if ch == '\n' {
			c.Lines++
		}

		if unicode.IsSpace(ch) {
			inWord = false
			continue
		}
		if !inWord {
			c.Words++
			inWord = true
		}
	}

	c.FinalNewline = !sawAny || lastRune == '\n'
	return c, nil
}
