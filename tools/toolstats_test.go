// ClawEh
// License: MIT

package tools

import (
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"unicode/utf8"
)

func TestToolStats_RoundTrip(t *testing.T) {
	const key = "agent:alice:subagent:round-trip"
	BeginToolStats(key)
	RecordToolResult(key, "file_read_lines", NewToolResult("ok"))
	RecordToolResult(key, "web_fetch", ErrorResult("first failure"))
	RecordToolResult(key, "maestro_task_get", nil)
	RecordToolResult(key, "file_write", &ToolResult{IsError: true, Err: errors.New("disk full")})

	calls, errs, last := EndToolStats(key)
	if calls != 4 || errs != 3 {
		t.Errorf("calls=%d errors=%d, want 4 and 3", calls, errs)
	}
	if last != "file_write: disk full" {
		t.Errorf("last error = %q, want %q", last, "file_write: disk full")
	}
}

func TestToolStats_NilResultText(t *testing.T) {
	const key = "agent:alice:subagent:nil-result"
	BeginToolStats(key)
	RecordToolResult(key, "maestro_task_get", nil)
	_, _, last := EndToolStats(key)
	if last != "maestro_task_get: tool returned nil result" {
		t.Errorf("last error = %q", last)
	}
}

func TestToolStats_UnregisteredKeyIsNoOp(t *testing.T) {
	const key = "agent:bob:main"
	RecordToolResult(key, "web_fetch", ErrorResult("boom"))
	RecordToolResult("", "web_fetch", ErrorResult("boom"))
	if calls, errs, last := EndToolStats(key); calls != 0 || errs != 0 || last != "" {
		t.Errorf("unregistered key tallied: %d %d %q", calls, errs, last)
	}
}

func TestToolStats_ZerosAfterEnd(t *testing.T) {
	const key = "agent:alice:subagent:ended"
	BeginToolStats(key)
	RecordToolResult(key, "web_fetch", ErrorResult("boom"))
	if calls, _, _ := EndToolStats(key); calls != 1 {
		t.Fatalf("calls = %d, want 1", calls)
	}
	// Recording after End must not resurrect the entry.
	RecordToolResult(key, "web_fetch", ErrorResult("boom"))
	if calls, errs, last := EndToolStats(key); calls != 0 || errs != 0 || last != "" {
		t.Errorf("after End: %d %d %q, want zeros", calls, errs, last)
	}
}

func TestToolStats_LastErrorRedactedAndTruncated(t *testing.T) {
	const key = "agent:alice:subagent:redact"
	token := "SST" + strings.Repeat("a", 64)
	BeginToolStats(key)
	RecordToolResult(key, "cogmem_status", ErrorResult("rejected "+token+" "+strings.Repeat("x", 1000)))
	_, _, last := EndToolStats(key)
	if strings.Contains(last, token) {
		t.Errorf("token not redacted: %q", last)
	}
	if !strings.Contains(last, "[REDACTED]") {
		t.Errorf("redaction marker missing: %q", last)
	}
	text := strings.TrimPrefix(last, "cogmem_status: ")
	if n := utf8.RuneCountInString(text); n != maxLastToolErrorRunes+1 {
		t.Errorf("error text is %d runes, want %d plus the ellipsis", n, maxLastToolErrorRunes)
	}
}

func TestToolStats_ConcurrentRecord(t *testing.T) {
	const key = "agent:alice:subagent:concurrent"
	const workers, perWorker = 16, 50
	BeginToolStats(key)
	var wg sync.WaitGroup
	for w := range workers {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for i := range perWorker {
				if i%2 == 0 {
					RecordToolResult(key, "web_fetch", ErrorResult(fmt.Sprintf("fail %d/%d", w, i)))
				} else {
					RecordToolResult(key, "web_fetch", NewToolResult("ok"))
				}
			}
		}(w)
	}
	wg.Wait()
	calls, errs, last := EndToolStats(key)
	if calls != workers*perWorker || errs != workers*perWorker/2 {
		t.Errorf("calls=%d errors=%d, want %d and %d", calls, errs, workers*perWorker, workers*perWorker/2)
	}
	if !strings.HasPrefix(last, "web_fetch: fail ") {
		t.Errorf("last error = %q", last)
	}
}
