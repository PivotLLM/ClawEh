package matrix

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"runtime"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"maunium.net/go/mautrix/id"

	"github.com/PivotLLM/ClawEh/bus"
	"github.com/PivotLLM/ClawEh/config"
)

// failAfter bounds a wait for something the test expects to happen; it is a
// failure guard, never what the outcome depends on.
const failAfter = 5 * time.Second

// resetAccounts forgets the process-wide account state for one test.
func resetAccounts(t *testing.T) {
	t.Helper()
	reset := func() {
		current.Lock()
		current.st = nil
		current.Unlock()
	}
	reset()
	t.Cleanup(reset)
}

// firstStart is the current account's first start.
func firstStart() time.Time {
	current.Lock()
	st := current.st
	current.Unlock()
	st.mu.Lock()
	defer st.mu.Unlock()
	return st.firstStart
}

// textEvent is a timeline message event from Alice.
func textEvent(eventID, body string, ts time.Time) string {
	return fmt.Sprintf(`{"type":"m.room.message","sender":"@alice:example.org","event_id":%q,"origin_server_ts":%d,"content":{"msgtype":"m.text","body":%q}}`,
		eventID, ts.UnixMilli(), body)
}

// syncBody is a /sync response with the events in one room.
func syncBody(next string, events ...string) string {
	if len(events) == 0 {
		return fmt.Sprintf(`{"next_batch":%q}`, next)
	}
	return fmt.Sprintf(`{"next_batch":%q,"rooms":{"join":{"!room:example.org":{"timeline":{"events":[%s]}}}}}`,
		next, strings.Join(events, ","))
}

// fakeHomeserver serves /sync from respond, which gets the request's since
// token and how often that token was asked for (from 1); a "" body parks the
// request until the client gives up. Every /sync request's since is sent on
// syncs. While blockMembers is set, a room-members lookup signals
// membersBlocked and parks; otherwise it is not found.
type fakeHomeserver struct {
	url            string
	syncs          chan string
	membersBlocked chan struct{}
	blockMembers   atomic.Bool

	mu      sync.Mutex
	asked   map[string]int
	respond func(since string, n int) string
}

func newFakeHomeserver(t *testing.T, respond func(since string, n int) string) *fakeHomeserver {
	t.Helper()
	hs := &fakeHomeserver{
		syncs:          make(chan string, 64),
		membersBlocked: make(chan struct{}, 8),
		asked:          map[string]int{},
		respond:        respond,
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/filter"):
			reply(t, w, `{"filter_id":"1"}`)
		case strings.HasSuffix(r.URL.Path, "/sync"):
			since := r.URL.Query().Get("since")
			hs.mu.Lock()
			hs.asked[since]++
			n := hs.asked[since]
			hs.mu.Unlock()
			hs.syncs <- since
			body := hs.respond(since, n)
			if body == "" {
				<-r.Context().Done()
				return
			}
			reply(t, w, body)
		case strings.HasSuffix(r.URL.Path, "/joined_members") && hs.blockMembers.Load():
			hs.membersBlocked <- struct{}{}
			<-r.Context().Done()
		default:
			w.WriteHeader(http.StatusNotFound)
			reply(t, w, `{"errcode":"M_NOT_FOUND","error":"not found"}`)
		}
	}))
	t.Cleanup(srv.Close)
	hs.url = srv.URL
	return hs
}

// awaitSync waits for a /sync request with the given since token. Once it
// arrives, every earlier response has been fully handled.
func (hs *fakeHomeserver) awaitSync(t *testing.T, since string) {
	t.Helper()
	deadline := time.After(failAfter)
	for {
		select {
		case got := <-hs.syncs:
			if got == since {
				return
			}
		case <-deadline:
			t.Fatalf("no sync since %q", since)
		}
	}
}

func (hs *fakeHomeserver) channel(t *testing.T, mb *bus.MessageBus) *MatrixChannel {
	t.Helper()
	c, err := NewMatrixChannel(config.MatrixConfig{
		Homeserver:  hs.url,
		UserID:      "@claw:matrix.test",
		AccessToken: "token",
		AllowFrom:   config.FlexibleStringSlice{"*"},
	}, mb)
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	return c
}

func stopChannel(t *testing.T, c *MatrixChannel) {
	t.Helper()
	if err := c.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
}

// drainInbound returns what the bus holds: messages are published while
// their sync response is handled, so after awaitSync they are all there.
func drainInbound(t *testing.T, mb *bus.MessageBus) []string {
	t.Helper()
	const end = "\x00end"
	if err := mb.PublishInbound(context.Background(), bus.InboundMessage{Content: end}); err != nil {
		t.Fatal(err)
	}
	var got []string
	for {
		ctx, cancel := context.WithTimeout(context.Background(), failAfter)
		msg, ok := mb.ConsumeInbound(ctx)
		cancel()
		if !ok {
			t.Fatal("bus closed or empty before the end marker")
		}
		if msg.Content == end {
			return got
		}
		got = append(got, msg.Content)
	}
}

// A channel rebuilt by a configuration save handles what arrived while it was
// being rebuilt, never an event from before the channel first started, and
// never an event its predecessor already handled.
func TestRebuiltChannel_ResumesWithoutLossOrDuplicates(t *testing.T) {
	resetAccounts(t)
	hs := newFakeHomeserver(t, func(since string, n int) string {
		start := firstStart()
		switch {
		case since == "":
			return syncBody("s1",
				textEvent("$old1", "before start", start.Add(-time.Minute)),
				textEvent("$a", "first", start.Add(time.Millisecond)))
		case since == "s1" && n == 2:
			return syncBody("s2",
				textEvent("$a", "first again", start.Add(time.Millisecond)),
				textEvent("$b", "during restart", time.Now()),
				textEvent("$old2", "also before start", start.Add(-time.Second)))
		}
		return ""
	})
	mb := bus.NewMessageBus()
	defer mb.Close()

	c1 := hs.channel(t, mb)
	hs.awaitSync(t, "s1")
	stopChannel(t, c1)
	if got := drainInbound(t, mb); !slices.Equal(got, []string{"first"}) {
		t.Fatalf("first channel delivered %q, want [first]", got)
	}

	c2 := hs.channel(t, mb)
	defer stopChannel(t, c2)
	if !c2.startTime.Equal(c1.startTime) {
		t.Errorf("rebuilt channel start = %v, want the first start %v", c2.startTime, c1.startTime)
	}
	hs.awaitSync(t, "s2")
	if got := drainInbound(t, mb); !slices.Equal(got, []string{"during restart"}) {
		t.Fatalf("rebuilt channel delivered %q, want [during restart]", got)
	}
}

// A channel stopped in the middle of a batch hands on nothing it had not
// finished: its successor fetches the batch again and handles the rest of it,
// each event exactly once.
func TestStopMidBatch_RestHandledOnceAfterRebuild(t *testing.T) {
	resetAccounts(t)
	var hs *fakeHomeserver
	hs = newFakeHomeserver(t, func(since string, n int) string {
		start := firstStart()
		switch since {
		case "":
			return syncBody("s1", textEvent("$e0", "zero", start.Add(time.Millisecond)))
		case "s1":
			// The first channel stalls on the first event of this batch.
			hs.blockMembers.Store(n == 1)
			return syncBody("s2",
				textEvent("$e0", "zero again", start.Add(time.Millisecond)),
				textEvent("$e1", "one", start.Add(2*time.Millisecond)),
				textEvent("$e2", "two", start.Add(3*time.Millisecond)))
		}
		return ""
	})
	mb := bus.NewMessageBus()
	defer mb.Close()

	c1 := hs.channel(t, mb)
	select {
	case <-hs.membersBlocked:
	case <-time.After(failAfter):
		t.Fatal("the first channel did not reach the stalled event")
	}
	stopChannel(t, c1)
	if got := drainInbound(t, mb); !slices.Equal(got, []string{"zero"}) {
		t.Fatalf("first channel delivered %q, want [zero]", got)
	}

	c2 := hs.channel(t, mb)
	defer stopChannel(t, c2)
	hs.awaitSync(t, "s2")
	if got := drainInbound(t, mb); !slices.Equal(got, []string{"one", "two"}) {
		t.Fatalf("rebuilt channel delivered %q, want [one two]", got)
	}
}

// A channel turned off (Removed) and on again starts fresh: it does not catch
// up on what was sent while it was off.
func TestRemovedChannel_StartsFresh(t *testing.T) {
	resetAccounts(t)
	var phase atomic.Int32
	hs := newFakeHomeserver(t, func(since string, _ int) string {
		start := firstStart()
		switch {
		case since == "" && phase.Load() == 0:
			return syncBody("s1", textEvent("$a", "first", start.Add(time.Millisecond)))
		case since == "" && phase.Load() == 1:
			// Sent while the channel was off: before the new channel's start.
			return syncBody("t1", textEvent("$b", "while off", start.Add(-time.Millisecond)))
		}
		return ""
	})
	mb := bus.NewMessageBus()
	defer mb.Close()

	c1 := hs.channel(t, mb)
	hs.awaitSync(t, "s1")
	stopChannel(t, c1)
	c1.Removed()
	if got := drainInbound(t, mb); !slices.Equal(got, []string{"first"}) {
		t.Fatalf("first channel delivered %q, want [first]", got)
	}

	phase.Store(1)
	c2 := hs.channel(t, mb)
	defer stopChannel(t, c2)
	hs.awaitSync(t, "t1") // a fresh sync (since ""), not a resume from s1
	if got := drainInbound(t, mb); len(got) != 0 {
		t.Fatalf("channel turned on again delivered %q, want nothing", got)
	}
}

// A message that could not be handed on (the bus is full and the channel is
// stopped meanwhile) is released, not recorded as handled, and the batch is
// not marked done: the rebuilt channel fetches it again and delivers it
// exactly once.
func TestHandOffFailure_RebuiltChannelDeliversOnce(t *testing.T) {
	resetAccounts(t)
	hs := newFakeHomeserver(t, func(since string, _ int) string {
		if since == "" {
			return syncBody("s1", textEvent("$x", "hello", firstStart().Add(time.Millisecond)))
		}
		return ""
	})
	mb := bus.NewMessageBus()
	defer mb.Close()
	// Fill the bus, so the first channel's hand-off blocks.
	const filler = "\x00filler"
	fillers := 0
	for {
		// Nothing consumes, so a publish times out only once the bus is full.
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
		err := mb.PublishInbound(ctx, bus.InboundMessage{Content: filler})
		cancel()
		if err != nil {
			break
		}
		fillers++
	}

	c1 := hs.channel(t, mb)
	deadline := time.Now().Add(failAfter)
	for !c1.account.handled("$x") {
		if time.Now().After(deadline) {
			t.Fatal("the first channel never claimed the event")
		}
		runtime.Gosched()
	}
	stopChannel(t, c1)
	if c1.account.handled("$x") {
		t.Fatal("an event that was not handed on is still recorded as handled")
	}
	if from, err := (accountSyncStore{c1.account}).LoadNextBatch(context.Background(), ""); err != nil || from != "" {
		t.Fatalf("resume point = %q, want the unfinished batch fetched again", from)
	}
	for range fillers {
		ctx, cancel := context.WithTimeout(context.Background(), failAfter)
		msg, ok := mb.ConsumeInbound(ctx)
		cancel()
		if !ok || msg.Content != filler {
			t.Fatalf("bus held %q, want only the fillers", msg.Content)
		}
	}

	c2 := hs.channel(t, mb)
	defer stopChannel(t, c2)
	hs.awaitSync(t, "s1")
	if got := drainInbound(t, mb); !slices.Equal(got, []string{"hello"}) {
		t.Fatalf("rebuilt channel delivered %q, want [hello] once", got)
	}
}

func eventIDs(ids ...string) []id.EventID {
	out := make([]id.EventID, len(ids))
	for i, s := range ids {
		out[i] = id.EventID(s)
	}
	return out
}

// release takes the event out of both the set and the order, so eviction
// later drops the right events.
func TestAccountState_ReleaseKeepsOrder(t *testing.T) {
	st := &accountState{seen: map[id.EventID]struct{}{}}
	for _, e := range eventIDs("$a", "$b", "$c") {
		st.claim(e)
	}
	st.release("$b")
	st.release("$unknown")
	if !slices.Equal(st.order, eventIDs("$a", "$c")) || len(st.seen) != 2 || st.handled("$b") {
		t.Fatalf("after release: order %v, seen %d, $b handled %v", st.order, len(st.seen), st.handled("$b"))
	}
	if !st.claim("$b") {
		t.Fatal("a released event cannot be claimed again")
	}
	if !slices.Equal(st.order, eventIDs("$a", "$c", "$b")) {
		t.Fatalf("order = %v, want [$a $c $b]", st.order)
	}
}

// Above seenEventsMax handled events, the oldest is forgotten first.
func TestAccountState_EvictsOldest(t *testing.T) {
	st := &accountState{seen: map[id.EventID]struct{}{}}
	for i := range seenEventsMax + 1 {
		if !st.claim(id.EventID(fmt.Sprintf("$%d", i))) {
			t.Fatalf("event %d claimed twice", i)
		}
	}
	if len(st.seen) != seenEventsMax || len(st.order) != seenEventsMax {
		t.Fatalf("remembers %d (order %d), want %d", len(st.seen), len(st.order), seenEventsMax)
	}
	if st.handled("$0") {
		t.Fatal("the oldest event was not forgotten")
	}
	if !st.handled("$1") || !st.handled(id.EventID(fmt.Sprintf("$%d", seenEventsMax))) {
		t.Fatal("a recent event was forgotten")
	}
}

// A channel for another account starts fresh, and so does the first account
// again afterwards: only one account is kept.
func TestAccountStateFor_SwitchingAccountStartsFresh(t *testing.T) {
	resetAccounts(t)
	alice := accountStateFor("https://hs.example", "@alice:example.org")
	alice.started(time.Unix(100, 0))
	alice.processed("s1")
	alice.claim("$a")
	if accountStateFor("https://hs.example", "@alice:example.org") != alice {
		t.Fatal("the same account did not keep its state")
	}

	bob := accountStateFor("https://hs.example", "@bob:example.org")
	if bob == alice || bob.resumeFrom != "" || !bob.firstStart.IsZero() || bob.handled("$a") {
		t.Fatal("switching account kept the previous account's state")
	}
	if again := accountStateFor("https://hs.example", "@alice:example.org"); again == alice || again.resumeFrom != "" {
		t.Fatal("switching back resumed state that was replaced")
	}
}

// forgetAccount of a state that has been replaced leaves the newer one alone.
func TestForgetAccount_OlderStateLeavesNewer(t *testing.T) {
	resetAccounts(t)
	alice := accountStateFor("https://hs.example", "@alice:example.org")
	bob := accountStateFor("https://hs.example", "@bob:example.org")
	forgetAccount(alice)
	if accountStateFor("https://hs.example", "@bob:example.org") != bob {
		t.Fatal("forgetting an older account dropped the current one")
	}
	forgetAccount(bob)
	if accountStateFor("https://hs.example", "@bob:example.org") == bob {
		t.Fatal("forgetting the current account kept it")
	}
}
