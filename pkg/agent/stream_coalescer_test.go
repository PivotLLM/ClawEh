// ClawEh
// License: MIT
//
// Copyright (c) 2026 Tenebris Technologies Inc.

package agent

import (
	"strings"
	"sync"
	"testing"
)

func TestCoalescerFlushOnSentenceBoundary(t *testing.T) {
	var mu sync.Mutex
	var batches []string
	c := newStreamCoalescer(func(b string) {
		mu.Lock()
		batches = append(batches, b)
		mu.Unlock()
	})

	// Tokens accumulate until a sentence-terminating rune flushes them.
	for _, tok := range []string{"Hel", "lo ", "wor", "ld."} {
		c.Add(tok)
	}
	if len(batches) != 1 || batches[0] != "Hello world." {
		t.Fatalf("expected one batch [Hello world.], got %v", batches)
	}

	// Trailing text with no boundary stays buffered until Flush.
	c.Add("more")
	if len(batches) != 1 {
		t.Fatalf("boundary-less text should not flush yet, got %v", batches)
	}
	c.Flush()
	if len(batches) != 2 || batches[1] != "more" {
		t.Fatalf("Flush should emit remainder [more], got %v", batches)
	}

	// A second Flush with an empty buffer is a no-op.
	c.Flush()
	if len(batches) != 2 {
		t.Fatalf("empty Flush should be a no-op, got %v", batches)
	}
}

func TestCoalescerFlushOnNewline(t *testing.T) {
	var batches []string
	c := newStreamCoalescer(func(b string) { batches = append(batches, b) })
	c.Add("line one\n")
	if len(batches) != 1 || batches[0] != "line one\n" {
		t.Fatalf("newline should trigger a flush, got %v", batches)
	}
}

func TestCoalescerFlushOnLength(t *testing.T) {
	var batches []string
	c := newStreamCoalescer(func(b string) { batches = append(batches, b) })

	// Feed boundary-less tokens past the length threshold.
	tok := "abcde " // 6 runes, no boundary
	total := 0
	for total < streamFlushThreshold+12 {
		c.Add(tok)
		total += len(tok)
	}
	if len(batches) == 0 {
		t.Fatalf("expected a length-triggered flush past %d chars", streamFlushThreshold)
	}
	// Each emitted batch (except a possible remainder after Flush) must be at least
	// the threshold length since only length triggered the flush.
	if got := len(batches[0]); got < streamFlushThreshold {
		t.Fatalf("first batch should be >= %d chars, got %d", streamFlushThreshold, got)
	}
	c.Flush()

	// No content is lost: rejoined batches equal the total fed.
	var joined strings.Builder
	for _, b := range batches {
		joined.WriteString(b)
	}
	if joined.Len() != total {
		t.Fatalf("expected %d total chars across batches, got %d", total, joined.Len())
	}
}

func TestCoalescerEmptyDeltaIgnored(t *testing.T) {
	var calls int
	c := newStreamCoalescer(func(string) { calls++ })
	c.Add("")
	c.Flush()
	if calls != 0 {
		t.Fatalf("empty delta then empty flush should never call sink, got %d calls", calls)
	}
}

// TestCoalescerConcurrentAddFlush exercises thread-safety: many goroutines Add
// while another Flushes. Run with -race. No content may be dropped or duplicated.
func TestCoalescerConcurrentAddFlush(t *testing.T) {
	const goroutines = 8
	const perGoroutine = 500

	var mu sync.Mutex
	var totalRunes int
	c := newStreamCoalescer(func(b string) {
		mu.Lock()
		totalRunes += len(b)
		mu.Unlock()
	})

	var wg sync.WaitGroup
	// Writers: each Adds a fixed-length token repeatedly.
	tok := "xy" // 2 runes, no boundary → exercises length/Flush paths
	for g := 0; g < goroutines; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < perGoroutine; i++ {
				c.Add(tok)
			}
		}()
	}
	// Concurrent flusher racing the writers.
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < perGoroutine; i++ {
			c.Flush()
		}
	}()
	wg.Wait()
	c.Flush() // drain any remainder after all writers finished

	want := goroutines * perGoroutine * len(tok)
	mu.Lock()
	got := totalRunes
	mu.Unlock()
	if got != want {
		t.Fatalf("concurrent Add/Flush lost or duplicated content: want %d runes, got %d", want, got)
	}
}
