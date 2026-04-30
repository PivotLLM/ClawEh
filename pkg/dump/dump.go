// Package dump writes diagnostic snapshots for unusual LLM responses.
package dump

import (
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Write creates a dump file in dumpsDir and returns the filename (not full path).
// The file contains four sections separated by "-----" lines:
//  1. Metadata (reason + caller-supplied metadata string)
//  2. Input
//  3. Output
//
// Returns ("", nil) if dumpsDir is empty (feature disabled).
// The dumps directory is created if it does not exist.
func Write(dumpsDir, reason, metadata, input, output string) (string, error) {
	if dumpsDir == "" {
		return "", nil
	}

	if err := os.MkdirAll(dumpsDir, 0o755); err != nil {
		return "", fmt.Errorf("dump: create dir: %w", err)
	}

	ts := time.Now().Format("20060102-150405")
	id := randID(6)
	filename := fmt.Sprintf("%s-%s.txt", ts, id)
	fullPath := filepath.Join(dumpsDir, filename)

	var sb strings.Builder
	sb.WriteString("REASON: " + reason + "\n")
	if metadata != "" {
		sb.WriteString(metadata)
		if !strings.HasSuffix(metadata, "\n") {
			sb.WriteString("\n")
		}
	}
	sb.WriteString("\n-----\n\nINPUT\n\n-----\n\n")
	sb.WriteString(input)
	if !strings.HasSuffix(input, "\n") {
		sb.WriteString("\n")
	}
	sb.WriteString("\n-----\n\nOUTPUT\n\n-----\n\n")
	sb.WriteString(output)
	if !strings.HasSuffix(output, "\n") {
		sb.WriteString("\n")
	}

	if err := os.WriteFile(fullPath, []byte(sb.String()), 0o644); err != nil {
		return "", fmt.Errorf("dump: write file: %w", err)
	}
	return filename, nil
}

func randID(n int) string {
	const chars = "abcdefghijklmnopqrstuvwxyz0123456789"
	b := make([]byte, n)
	for i := range b {
		b[i] = chars[rand.Intn(len(chars))]
	}
	return string(b)
}
