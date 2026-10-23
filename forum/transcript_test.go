// ClawEh
// License: MIT
//
// Copyright (c) 2026 Tenebris Technologies Inc.

package forum

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// The transcript is exactly the public entries, in publication order:
// the store writes nothing to it but what AppendTranscript is given.
func TestTranscriptOrderAndContent(t *testing.T) {
	r := rpFullRun(t)
	data, err := r.s.ReadFile(fileTranscript)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := string(data), strings.Join(r.public, ""); got != want {
		t.Fatalf("transcript:\n%s\nwant:\n%s", got, want)
	}
	stIsPerm(t, r.s.Path(fileTranscript), filePerm)
}

// Private material (instructions, the messages sent, rejected attempts,
// unshared output members, the moderator's assessment and directed
// messages) is kept on disk but never reaches transcript.md.
func TestTranscriptHoldsNoPrivateMaterial(t *testing.T) {
	r := rpFullRun(t)
	transcript, err := r.s.ReadFile(fileTranscript)
	if err != nil {
		t.Fatal(err)
	}
	// Everything else under the root, to prove each marker was really
	// written somewhere, so its absence from the transcript means something.
	var elsewhere strings.Builder
	err = filepath.WalkDir(r.s.Root(), func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || d.Name() == fileTranscript {
			return err
		}
		data, err := os.ReadFile(p)
		elsewhere.Write(data)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(r.private) < 10 {
		t.Fatalf("only %d private markers; the run did not exercise the private paths", len(r.private))
	}
	for _, marker := range r.private {
		if strings.Contains(string(transcript), marker) {
			t.Errorf("transcript contains private material %q", marker)
		}
		if !strings.Contains(elsewhere.String(), marker) {
			t.Errorf("private material %q was not retained anywhere", marker)
		}
	}
}

// Concurrent appends never interleave and keep each writer's order.
func TestTranscriptConcurrentAppends(t *testing.T) {
	s := stNewStore(t)
	const writers, entries = 8, 25
	var wg sync.WaitGroup
	for w := range writers {
		wg.Go(func() {
			for e := range entries {
				entry := fmt.Sprintf("w%d-e%d first\nw%d-e%d second\n", w, e, w, e)
				if err := s.AppendTranscript(entry); err != nil {
					t.Error(err)
					return
				}
			}
		})
	}
	wg.Wait()
	data, err := s.ReadFile(fileTranscript)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSuffix(string(data), "\n"), "\n")
	if len(lines) != 2*writers*entries {
		t.Fatalf("%d lines, want %d", len(lines), 2*writers*entries)
	}
	next := make([]int, writers)
	for i := 0; i < len(lines); i += 2 {
		var w, e int
		if _, err := fmt.Sscanf(lines[i], "w%d-e%d first", &w, &e); err != nil {
			t.Fatalf("line %d %q: %v", i, lines[i], err)
		}
		if lines[i+1] != fmt.Sprintf("w%d-e%d second", w, e) {
			t.Fatalf("entry w%d-e%d interleaved: %q", w, e, lines[i+1])
		}
		if e != next[w] {
			t.Fatalf("writer %d: entry %d arrived before %d", w, e, next[w])
		}
		next[w]++
	}
}
