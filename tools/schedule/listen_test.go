// ClawEh
// License: MIT

package schedule

import (
	"context"
	"fmt"
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

// TestListen_DeliversEvents is the core behaviour: the full result is
// delivered with a note saying where it came from, a result without the
// watched field is not delivered, a timed-out wait is neither delivered nor
// counted as a failure, and — by default — a repeated event is delivered
// again, because each occurrence may matter.
func TestListen_DeliversEvents(t *testing.T) {
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
	for _, want := range []string{"continuous monitor at", "A document event arrived.", "documents_event_wait returned the following:", `"title":"first"`} {
		if !strings.Contains(first.Content, want) {
			t.Fatalf("delivered message missing %q:\n%s", want, first.Content)
		}
	}
	if strings.Contains(first.Content, "cron job that fired") {
		t.Fatalf("monitor event was wrapped as a cron fire:\n%s", first.Content)
	}
	if first.Channel != "telegram-Amber" || first.ChatID != "chat-amber" || first.SenderID != "cron" {
		t.Fatalf("delivered to %s/%s as %s, want amber's default channel", first.Channel, first.ChatID, first.SenderID)
	}

	// The replay of e1 is delivered again: repeats are on by default.
	replay, ok := nextInbound(t, msgBus, 3*time.Second)
	if !ok || !strings.Contains(replay.Content, `"id":"e1"`) {
		t.Fatalf("repeated e1 not delivered by default: ok=%v\n%s", ok, replay.Content)
	}
	third, ok := nextInbound(t, msgBus, 5*time.Second)
	if !ok {
		t.Fatal("e2 not delivered")
	}
	if !strings.Contains(third.Content, `"id":"e2"`) {
		t.Fatalf("third delivery is not e2:\n%s", third.Content)
	}
	// The no-data result and the timeout produced nothing.
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

// TestListen_ToolActionsReconcileImmediately: with the ticker effectively off,
// a listener still starts on add and stops on remove, because the tool kicks
// the supervisor.
func TestListen_ToolActionsReconcileImmediately(t *testing.T) {
	tool := &scriptedTool{}
	ct, _ := newListenEnv(t, tool)
	listenReconcileInterval = time.Hour

	ct.StartListeners(context.Background())
	defer ct.StopListeners()

	running := func() int {
		ct.listenMu.Lock()
		defer ct.listenMu.Unlock()
		return len(ct.listeners)
	}
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

	job := addListenJob(t, ct, nil)
	waitFor(func() bool { return running() == 1 && tool.callCount() >= 1 }, "listener did not start on add")

	res := ct.Execute(agentCtx("amber"), map[string]any{"action": "remove", "job_id": job.ID})
	if res.IsError {
		t.Fatalf("remove: %s", res.ForLLM)
	}
	waitFor(func() bool { return running() == 0 }, "listener did not stop on remove")
}

// TestListen_FailedDeliveryDoesNotAdvance: an event that could not be put on
// the bus is not marked delivered, so a source that replays it is not silenced.
func TestListen_FailedDeliveryDoesNotAdvance(t *testing.T) {
	tool := &scriptedTool{replies: []scriptedReply{{text: `{"event":{"id":"e1"}}`}}}
	ct, msgBus := newListenEnv(t, tool)
	job := addListenJob(t, ct, nil)
	msgBus.Close() // every publish now fails at once

	ct.StartListeners(context.Background())
	deadline := time.Now().Add(3 * time.Second)
	for tool.callCount() < 2 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	ct.StopListeners()
	if tool.callCount() < 2 {
		t.Fatal("listener did not get past the failed delivery")
	}
	jobs := ct.cronService.ListJobs(true)
	if jobs[0].State.WatchDigest != "" {
		t.Fatalf("fingerprint advanced to %q after a failed delivery", jobs[0].State.WatchDigest)
	}
	_ = job
}

// TestListen_DistinctEventsAllDelivered: a burst of different events is
// delivered one by one, in order, none dropped.
func TestListen_DistinctEventsAllDelivered(t *testing.T) {
	var replies []scriptedReply
	for i := 0; i < 10; i++ {
		replies = append(replies, scriptedReply{text: fmt.Sprintf(`{"event":{"id":"e%d"}}`, i)})
	}
	ct, msgBus := newListenEnv(t, &scriptedTool{replies: replies})
	addListenJob(t, ct, nil)
	ct.StartListeners(context.Background())
	defer ct.StopListeners()

	for i := 0; i < 10; i++ {
		msg, ok := nextInbound(t, msgBus, 3*time.Second)
		if !ok {
			t.Fatalf("event %d not delivered", i)
		}
		if want := fmt.Sprintf(`"id":"e%d"`, i); !strings.Contains(msg.Content, want) {
			t.Fatalf("event %d out of order or wrong:\n%s", i, msg.Content)
		}
		if msg.Channel != "telegram-Amber" || msg.ChatID != "chat-amber" {
			t.Fatalf("event %d delivered to %s/%s", i, msg.Channel, msg.ChatID)
		}
	}
}

// TestListen_RepeatsDeliveredByDefault: the same event three times in a row
// reaches the agent three times, with no option set.
func TestListen_RepeatsDeliveredByDefault(t *testing.T) {
	same := scriptedReply{text: `{"event":{"id":"doc-7","action":"edited"}}`}
	ct, msgBus := newListenEnv(t, &scriptedTool{replies: []scriptedReply{same, same, same}})
	job := addListenJob(t, ct, nil)
	if job.Payload.Watch.SuppressRepeats {
		t.Fatal("suppression is on without being asked for")
	}
	ct.StartListeners(context.Background())
	defer ct.StopListeners()
	for i := 0; i < 3; i++ {
		msg, ok := nextInbound(t, msgBus, 3*time.Second)
		if !ok {
			t.Fatalf("repeat %d not delivered", i+1)
		}
		if !strings.Contains(msg.Content, `"id":"doc-7"`) {
			t.Fatalf("repeat %d wrong:\n%s", i+1, msg.Content)
		}
	}
	if extra, ok := nextInbound(t, msgBus, 200*time.Millisecond); ok {
		t.Fatalf("delivered more than the three events:\n%s", extra.Content)
	}
}

// TestListen_SuppressRepeatsOptIn: with suppress_repeats the same event three
// times in a row reaches the agent once.
func TestListen_SuppressRepeatsOptIn(t *testing.T) {
	same := scriptedReply{text: `{"event":{"id":"doc-7","action":"edited"}}`}
	ct, msgBus := newListenEnv(t, &scriptedTool{replies: []scriptedReply{same, same, same}})
	job := addListenJob(t, ct, map[string]any{"suppress_repeats": true})
	if !job.Payload.Watch.SuppressRepeats {
		t.Fatal("suppress_repeats not recorded on the job")
	}
	ct.StartListeners(context.Background())
	defer ct.StopListeners()
	if _, ok := nextInbound(t, msgBus, 3*time.Second); !ok {
		t.Fatal("first event not delivered")
	}
	if extra, ok := nextInbound(t, msgBus, 300*time.Millisecond); ok {
		t.Fatalf("repeat delivered despite suppress_repeats:\n%s", extra.Content)
	}
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
