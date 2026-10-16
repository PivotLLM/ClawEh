package matrix

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/PivotLLM/ClawEh/bus"
	"github.com/PivotLLM/ClawEh/config"
)

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

func newTestMatrix(t *testing.T, url string, mb *bus.MessageBus) *MatrixChannel {
	t.Helper()
	c, err := NewMatrixChannel(config.MatrixConfig{
		Homeserver:  url,
		UserID:      "@claw:matrix.test",
		AccessToken: "token",
		AllowFrom:   config.FlexibleStringSlice{"*"},
	}, mb)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

// inboundContents drains the bus for a short while and returns what arrived.
func inboundContents(mb *bus.MessageBus, within time.Duration) []string {
	var got []string
	for {
		ctx, cancel := context.WithTimeout(context.Background(), within)
		msg, ok := mb.ConsumeInbound(ctx)
		cancel()
		if !ok {
			return got
		}
		got = append(got, msg.Content)
	}
}

// A channel rebuilt by a configuration save handles what arrived while it was
// being rebuilt, never an event from before ClawEh started, and never an event
// its predecessor already handled.
func TestRebuiltChannel_ResumesWithoutLossOrDuplicates(t *testing.T) {
	var (
		syncs   atomic.Int32
		sinceS1 atomic.Int32
		first   atomic.Pointer[time.Time]
	)
	var url string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/filter"):
			reply(t, w, `{"filter_id":"1"}`)
		case strings.HasSuffix(r.URL.Path, "/sync"):
			syncs.Add(1)
			start := accountStateFor(url, "@claw:matrix.test").started(time.Now())
			first.Store(&start)
			switch r.URL.Query().Get("since") {
			case "":
				reply(t, w, syncBody("s1",
					textEvent("$old1", "before start", start.Add(-time.Minute)),
					textEvent("$a", "first", start.Add(time.Millisecond))))
			case "s1":
				if sinceS1.Add(1) == 1 {
					// Only the rebuilt channel gets past here: the first one is
					// stopped once s1 is handled (see below), and it parks here.
					<-r.Context().Done()
					return
				}
				reply(t, w, syncBody("s2",
					textEvent("$a", "first again", start.Add(time.Millisecond)),
					textEvent("$b", "during restart", time.Now()),
					textEvent("$old2", "also before start", start.Add(-time.Second))))
			default:
				<-r.Context().Done()
			}
		default:
			w.WriteHeader(http.StatusNotFound)
			reply(t, w, `{"errcode":"M_NOT_FOUND","error":"not found"}`)
		}
	}))
	t.Cleanup(srv.Close)
	url = srv.URL

	mb := bus.NewMessageBus()
	defer mb.Close()

	c1 := newTestMatrix(t, url, mb)
	if err := c1.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	// s1 is handled once the channel asks for what follows it.
	waitFor(t, "sync after s1", 5*time.Second, func() bool { return sinceS1.Load() >= 1 })
	if err := c1.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := inboundContents(mb, 100*time.Millisecond); len(got) != 1 || got[0] != "first" {
		t.Fatalf("first channel delivered %q, want [first]", got)
	}

	c2 := newTestMatrix(t, url, mb)
	if err := c2.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := c2.Stop(context.Background()); err != nil {
			t.Error(err)
		}
	})
	if !c2.startTime.Equal(*first.Load()) {
		t.Errorf("rebuilt channel start = %v, want the first start %v", c2.startTime, *first.Load())
	}
	waitFor(t, "sync after s2", 5*time.Second, func() bool { return syncs.Load() >= 4 })
	if got := inboundContents(mb, 100*time.Millisecond); len(got) != 1 || got[0] != "during restart" {
		t.Fatalf("rebuilt channel delivered %q, want [during restart]", got)
	}
}
