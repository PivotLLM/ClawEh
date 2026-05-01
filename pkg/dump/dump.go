// Package dump writes diagnostic snapshots for unusual or inspected LLM responses.
// Each dump produces two files with a shared base name:
//   - YYYYMMDD-HHMMSS-<id>.json  — structured JSON for programmatic parsing
//   - YYYYMMDD-HHMMSS-<id>.txt   — human-readable pretty-printed version
package dump

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// dumpDoc is the top-level structure written to the .json file.
type dumpDoc struct {
	Metadata map[string]any  `json:"metadata"`
	Input    json.RawMessage `json:"input"`
	Output   json.RawMessage `json:"output"`
}

// Write creates a .json and .txt dump file pair in dumpsDir and returns the
// base filename (without extension). Returns ("", nil) if dumpsDir is empty.
// The dumps directory is created if it does not exist.
//
// metadata must not include a "reason" key — reason is added automatically.
// input and output must be valid JSON (typically from json.Marshal).
func Write(dumpsDir, reason string, metadata map[string]any, input, output json.RawMessage) (string, error) {
	if dumpsDir == "" {
		return "", nil
	}

	if err := os.MkdirAll(dumpsDir, 0o755); err != nil {
		return "", fmt.Errorf("dump: create dir: %w", err)
	}

	ts := time.Now().Format("20060102-150405")
	id := randID(6)
	basename := fmt.Sprintf("%s-%s", ts, id)

	// Merge reason into metadata for the JSON doc.
	meta := make(map[string]any, len(metadata)+1)
	meta["reason"] = reason
	for k, v := range metadata {
		meta[k] = v
	}

	doc := dumpDoc{
		Metadata: meta,
		Input:    input,
		Output:   output,
	}

	// --- .json file ---
	var jsonBuf bytes.Buffer
	enc := json.NewEncoder(&jsonBuf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(doc); err != nil {
		return "", fmt.Errorf("dump: marshal json: %w", err)
	}
	if err := os.WriteFile(filepath.Join(dumpsDir, basename+".json"), jsonBuf.Bytes(), 0o644); err != nil {
		return "", fmt.Errorf("dump: write json: %w", err)
	}

	// --- .txt file ---
	txt := buildTxt(reason, meta, input, output)
	if err := os.WriteFile(filepath.Join(dumpsDir, basename+".txt"), []byte(txt), 0o644); err != nil {
		return "", fmt.Errorf("dump: write txt: %w", err)
	}

	return basename, nil
}

// buildTxt produces a human-readable dump with three sections separated by
// "-----" markers. JSON blocks are pretty-printed and escaped \n sequences
// within string values are converted to real newlines for readability.
func buildTxt(reason string, meta map[string]any, input, output json.RawMessage) string {
	var sb strings.Builder

	// Section 1: metadata
	sb.WriteString("REASON: " + reason + "\n")
	// Write remaining metadata keys in a stable order, reason already shown.
	orderedKeys := []string{"agent", "model", "session", "channel", "iteration", "finish_reason", "timestamp"}
	written := map[string]bool{"reason": true}
	for _, k := range orderedKeys {
		if v, ok := meta[k]; ok {
			sb.WriteString(fmt.Sprintf("%s: %v\n", k, v))
			written[k] = true
		}
	}
	// Any remaining keys not in the ordered list.
	for k, v := range meta {
		if !written[k] {
			sb.WriteString(fmt.Sprintf("%s: %v\n", k, v))
		}
	}

	// Section 2: input
	sb.WriteString("\n-----\n\nINPUT\n\n-----\n\n")
	sb.WriteString(prettyJSON(input))
	sb.WriteString("\n")

	// Section 3: output
	sb.WriteString("\n-----\n\nOUTPUT\n\n-----\n\n")
	sb.WriteString(prettyJSON(output))
	sb.WriteString("\n")

	return sb.String()
}

// prettyJSON returns indented JSON with HTML escaping disabled and \n sequences
// inside string values replaced with real newlines, for human readability.
func prettyJSON(raw json.RawMessage) string {
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		return string(raw)
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(v); err != nil {
		return string(raw)
	}
	// Encoder.Encode appends a trailing newline; strip it — callers add their own.
	result := strings.TrimRight(buf.String(), "\n")
	// Replace escaped \n in string values with real newlines so multi-line
	// content (system prompts, message bodies) is legible in the txt file.
	return strings.ReplaceAll(result, `\n`, "\n")
}

func randID(n int) string {
	const chars = "abcdefghijklmnopqrstuvwxyz0123456789"
	b := make([]byte, n)
	for i := range b {
		b[i] = chars[rand.Intn(len(chars))]
	}
	return string(b)
}
