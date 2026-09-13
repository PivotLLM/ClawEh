// ClawEh
// License: MIT

package schedule

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/PivotLLM/ClawEh/bus"
	"github.com/PivotLLM/ClawEh/config"
	"github.com/PivotLLM/ClawEh/cron"
	"github.com/PivotLLM/ClawEh/tools"
)

// scriptedTool is a fake long-poll tool: each call pops the next scripted
// reply, or blocks until the context ends when the script is exhausted.
type scriptedTool struct {
	mu      sync.Mutex
	replies []scriptedReply
	calls   int
}

type scriptedReply struct {
	text  string
	err   bool
	block bool // hold until ctx ends, like a long-poll with no event
}

func (s *scriptedTool) Name() string               { return "documents_event_wait" }
func (s *scriptedTool) Description() string        { return "fake long-poll" }
func (s *scriptedTool) Parameters() map[string]any { return map[string]any{"type": "object"} }
func (s *scriptedTool) Execute(ctx context.Context, _ map[string]any) *tools.ToolResult {
	s.mu.Lock()
	s.calls++
	var r scriptedReply
	if len(s.replies) > 0 {
		r, s.replies = s.replies[0], s.replies[1:]
	} else {
		r = scriptedReply{block: true}
	}
	s.mu.Unlock()
	if r.block {
		<-ctx.Done()
		return tools.ErrorResult(ctx.Err().Error())
	}
	if r.err {
		return tools.ErrorResult(r.text)
	}
	return tools.NewToolResult(r.text)
}

func (s *scriptedTool) callCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.calls
}

// newListenEnv builds a cron tool whose agent "amber" owns one scripted tool,
// with timing levers shortened so a test runs in milliseconds.
func newListenEnv(t *testing.T, tool *scriptedTool) (*CronTool, *bus.MessageBus) {
	t.Helper()
	oldMin, oldBackoff, oldReconcile := listenMinInterval, listenBackoffBase, listenReconcileInterval
	listenMinInterval, listenBackoffBase, listenReconcileInterval = 10*time.Millisecond, 10*time.Millisecond, 20*time.Millisecond
	t.Cleanup(func() {
		listenMinInterval, listenBackoffBase, listenReconcileInterval = oldMin, oldBackoff, oldReconcile
	})

	msgBus := bus.NewMessageBus()
	cfg := testConfig()
	cs := cron.NewCronService(t.TempDir()+"/cron.json", nil)
	ct := NewCronTool(cs, msgBus, func() *config.Config { return cfg })
	reg := tools.NewToolRegistry()
	reg.Register(tool)
	ct.SetAgentTools(func(agentID string) *tools.ToolRegistry {
		if agentID == "amber" {
			return reg
		}
		return nil
	})
	return ct, msgBus
}

func addListenJob(t *testing.T, ct *CronTool, args map[string]any) *cron.CronJob {
	t.Helper()
	base := map[string]any{
		"action": "add", "message": "A document event arrived.", "listen": true,
		"watch_tool": "documents_event_wait", "watch_fields": []any{"event.id"},
		"watch_timeout_seconds": float64(1),
	}
	for k, v := range args {
		base[k] = v
	}
	res := ct.Execute(agentCtx("amber"), base)
	if res.IsError {
		t.Fatalf("add listen job: %s", res.ForLLM)
	}
	jobs := ct.cronService.ListJobs(true)
	if len(jobs) != 1 {
		t.Fatalf("jobs = %d, want 1", len(jobs))
	}
	return &jobs[0]
}

func nextInbound(t *testing.T, msgBus *bus.MessageBus, within time.Duration) (bus.InboundMessage, bool) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), within)
	defer cancel()
	return msgBus.ConsumeInbound(ctx)
}

// TestListen_AddValidation: listen needs a watch tool and takes no schedule.
func TestListen_AddValidation(t *testing.T) {
	ct, _ := newListenEnv(t, &scriptedTool{})
	res := ct.Execute(agentCtx("amber"), map[string]any{"action": "add", "message": "m", "listen": true})
	if !res.IsError || !strings.Contains(res.ForLLM, "watch_tool") {
		t.Fatalf("listen without watch_tool: %+v", res)
	}
	res = ct.Execute(agentCtx("amber"), map[string]any{
		"action": "add", "message": "m", "listen": true, "watch_tool": "x", "every_seconds": float64(60),
	})
	if !res.IsError || !strings.Contains(res.ForLLM, "no schedule") {
		t.Fatalf("listen with a schedule: %+v", res)
	}
	job := addListenJob(t, ct, nil)
	if job.Schedule.Kind != cron.KindListen || job.Payload.Mode != cron.KindListen || job.State.NextRunAtMS != nil {
		t.Fatalf("job = %+v, want listen kind with no next run", job)
	}
	if job.Payload.Watch == nil || job.Payload.Watch.TimeoutSec != 1 {
		t.Fatalf("watch = %+v, want timeout 1s", job.Payload.Watch)
	}
	if !strings.Contains(formatSchedule(job), "listen") {
		t.Fatalf("formatSchedule = %q", formatSchedule(job))
	}
}

// TestListen_DeliversNewEventsOnce is the core behaviour: the full result is
// delivered with a note saying where it came from, a repeated event is not
// delivered twice, a result without the watched field is not delivered, and a
// timed-out wait is neither delivered nor counted as a failure.
func TestListen_DeliversNewEventsOnce(t *testing.T) {
	tool := &scriptedTool{replies: []scriptedReply{
		{text: `{"event":{"id":"e1","title":"first"}}`},
		{text: `{"event":{"id":"e1","title":"first"}}`}, // replayed on reconnect
		{text: `{"status":"waiting"}`},                  // no event field: no data
		{block: true},                                   // long-poll timeout
		{text: `{"event":{"id":"e2","title":"second"}}`},
	}}
	ct, msgBus := newListenEnv(t, tool)
	job := addListenJob(t, ct, nil)

	ct.StartListeners(context.Background())
	defer ct.StopListeners()

	first, ok := nextInbound(t, msgBus, 3*time.Second)
	if !ok {
		t.Fatal("first event not delivered")
	}
	for _, want := range []string{"A document event arrived.", "documents_event_wait returned the following:", `"title":"first"`} {
		if !strings.Contains(first.Content, want) {
			t.Fatalf("delivered message missing %q:\n%s", want, first.Content)
		}
	}
	if first.Channel != "telegram-Amber" || first.ChatID != "chat-amber" || first.SenderID != "cron" {
		t.Fatalf("delivered to %s/%s as %s, want amber's default channel", first.Channel, first.ChatID, first.SenderID)
	}

	second, ok := nextInbound(t, msgBus, 5*time.Second)
	if !ok {
		t.Fatal("second event not delivered")
	}
	if !strings.Contains(second.Content, `"id":"e2"`) {
		t.Fatalf("second delivery is not e2:\n%s", second.Content)
	}
	// e1's replay, the no-data result and the timeout produced nothing.
	if extra, ok := nextInbound(t, msgBus, 200*time.Millisecond); ok {
		t.Fatalf("unexpected extra delivery:\n%s", extra.Content)
	}

	// The last delivered fingerprint is persisted so a restart does not replay e2.
	jobs := ct.cronService.ListJobs(true)
	if jobs[0].State.WatchDigest == "" || jobs[0].State.WatchFailures != 0 {
		t.Fatalf("state = %+v, want a digest and zero failures", jobs[0].State)
	}
	_ = job
}

// TestListen_FailuresBackOffAndNotifyOnce: errors retry with backoff and the
// agent hears about it exactly once, at the threshold.
func TestListen_FailuresBackOffAndNotifyOnce(t *testing.T) {
	var replies []scriptedReply
	for i := 0; i < watchFailureNotifyThreshold+2; i++ {
		replies = append(replies, scriptedReply{text: "connection dropped", err: true})
	}
	tool := &scriptedTool{replies: replies}
	ct, msgBus := newListenEnv(t, tool)
	addListenJob(t, ct, nil)

	ct.StartListeners(context.Background())
	defer ct.StopListeners()

	notice, ok := nextInbound(t, msgBus, 5*time.Second)
	if !ok {
		t.Fatal("failure notice not delivered")
	}
	if !strings.Contains(notice.Content, "failed 5 times in a row") {
		t.Fatalf("notice = %s", notice.Content)
	}
	if extra, ok := nextInbound(t, msgBus, 300*time.Millisecond); ok {
		t.Fatalf("second notice delivered:\n%s", extra.Content)
	}
	if listenBackoff(1) != listenBackoffBase || listenBackoff(3) != 4*listenBackoffBase || listenBackoff(100) != listenBackoffMax {
		t.Fatalf("backoff curve wrong: %v %v %v", listenBackoff(1), listenBackoff(3), listenBackoff(100))
	}
}

// TestListen_ReconcileStopsDisabledJobs: disabling the job stops its loop
// within a reconcile interval, and re-enabling restarts it.
func TestListen_ReconcileStopsDisabledJobs(t *testing.T) {
	tool := &scriptedTool{}
	ct, _ := newListenEnv(t, tool)
	job := addListenJob(t, ct, nil)

	ct.StartListeners(context.Background())
	defer ct.StopListeners()

	waitFor := func(cond func() bool, what string) {
		t.Helper()
		deadline := time.Now().Add(3 * time.Second)
		for time.Now().Before(deadline) {
			if cond() {
				return
			}
			time.Sleep(10 * time.Millisecond)
		}
		t.Fatal(what)
	}
	running := func() int {
		ct.listenMu.Lock()
		defer ct.listenMu.Unlock()
		return len(ct.listeners)
	}
	waitFor(func() bool { return running() == 1 && tool.callCount() >= 1 }, "listener did not start")

	if _, err := ct.cronService.EnableJob(job.ID, false); err != nil {
		t.Fatal(err)
	}
	waitFor(func() bool { return running() == 0 }, "listener not stopped after disable")

	if _, err := ct.cronService.EnableJob(job.ID, true); err != nil {
		t.Fatal(err)
	}
	waitFor(func() bool { return running() == 1 }, "listener not restarted after enable")
}

// TestListen_ExecuteJobIsANoOp: the scheduler never fires a listen job, and
// if asked to it does nothing.
func TestListen_ExecuteJobIsANoOp(t *testing.T) {
	ct, msgBus := newListenEnv(t, &scriptedTool{})
	job := addListenJob(t, ct, nil)
	if out := ct.ExecuteJob(context.Background(), job); !strings.Contains(out, "listen") {
		t.Fatalf("ExecuteJob = %q", out)
	}
	if _, ok := nextInbound(t, msgBus, 100*time.Millisecond); ok {
		t.Fatal("ExecuteJob delivered for a listen job")
	}
}
