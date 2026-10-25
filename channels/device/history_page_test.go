package device

import (
	"encoding/json"
	"strconv"
	"testing"

	"github.com/PivotLLM/ClawEh/gatewayproto"
)

// transcript builds n messages whose text is their 1-based position, so a page
// can be checked by the numbers it carries.
func transcript(n int) []DeviceHistoryMessage {
	out := make([]DeviceHistoryMessage, 0, n)
	for i := 1; i <= n; i++ {
		role := "user"
		if i%2 == 0 {
			role = "assistant"
		}
		out = append(out, DeviceHistoryMessage{Role: role, Content: strconv.Itoa(i)})
	}
	return out
}

func texts(msgs []DeviceHistoryMessage) []string {
	out := make([]string, 0, len(msgs))
	for _, m := range msgs {
		out = append(out, m.Content)
	}
	return out
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// The window is counted from the newest message: limit takes the last messages,
// offset skips the most recent ones, and nextOffset is the offset of the page
// before the one returned.
func TestPageHistoryWindow(t *testing.T) {
	h := transcript(10)
	tests := []struct {
		name       string
		p          historyPageParams
		want       []string
		offset     int
		hasMore    bool
		nextOffset int
	}{
		{
			name: "no paging fields returns everything under the default limit",
			p:    historyPageParams{}, want: []string{"1", "2", "3", "4", "5", "6", "7", "8", "9", "10"},
		},
		{
			name: "limit takes the newest",
			p:    historyPageParams{Limit: new(3)}, want: []string{"8", "9", "10"}, hasMore: true, nextOffset: 3,
		},
		{
			name: "offset skips the newest",
			p:    historyPageParams{Limit: new(3), Offset: new(3)}, want: []string{"5", "6", "7"}, offset: 3, hasMore: true, nextOffset: 6,
		},
		{
			name: "last page is short and has no more",
			p:    historyPageParams{Limit: new(4), Offset: new(8)}, want: []string{"1", "2"}, offset: 8,
		},
		{
			name: "offset past the end is an empty page",
			p:    historyPageParams{Limit: new(4), Offset: new(50)}, want: []string{}, offset: 10,
		},
		{
			name: "limit larger than the transcript",
			p:    historyPageParams{Limit: new(1000)}, want: []string{"1", "2", "3", "4", "5", "6", "7", "8", "9", "10"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := pageHistory(h, tt.p)
			if err != nil {
				t.Fatalf("pageHistory: %v", err)
			}
			if !equalStrings(texts(got.Messages), tt.want) {
				t.Errorf("messages = %v, want %v", texts(got.Messages), tt.want)
			}
			if got.Offset != tt.offset || got.TotalMessages != 10 || got.HasMore != tt.hasMore {
				t.Errorf("offset/total/hasMore = %d/%d/%v, want %d/10/%v", got.Offset, got.TotalMessages, got.HasMore, tt.offset, tt.hasMore)
			}
			if tt.hasMore && got.NextOffset != tt.nextOffset {
				t.Errorf("nextOffset = %d, want %d", got.NextOffset, tt.nextOffset)
			}
		})
	}
}

// Paging back with nextOffset walks the whole transcript, newest page first,
// without a gap or an overlap.
func TestPageHistoryWalksBack(t *testing.T) {
	h := transcript(7)
	var seen []string
	offset := 0
	for {
		page, err := pageHistory(h, historyPageParams{Limit: new(3), Offset: new(offset)})
		if err != nil {
			t.Fatalf("pageHistory: %v", err)
		}
		seen = append(texts(page.Messages), seen...)
		if !page.HasMore {
			break
		}
		offset = page.NextOffset
	}
	if want := texts(h); !equalStrings(seen, want) {
		t.Fatalf("walked %v, want %v", seen, want)
	}
}

// Out-of-range values are refused rather than clamped, as the protocol schema
// does.
func TestPageHistoryRefusesBadParams(t *testing.T) {
	for name, p := range map[string]historyPageParams{
		"limit 0":          {Limit: new(0)},
		"limit over 1000":  {Limit: new(1001)},
		"negative offset":  {Offset: new(-1)},
		"maxChars 0":       {MaxChars: new(0)},
		"maxChars too big": {MaxChars: new(500_001)},
	} {
		if _, err := pageHistory(transcript(2), p); err == nil {
			t.Errorf("%s: accepted, want an error", name)
		}
	}
}

// The default limit is 200, as in the OpenClaw Gateway.
func TestPageHistoryDefaultLimit(t *testing.T) {
	page, err := pageHistory(transcript(250), historyPageParams{})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Messages) != 200 || page.Messages[0].Content != "51" || !page.HasMore || page.NextOffset != 200 {
		t.Fatalf("got %d messages from %q, hasMore %v, nextOffset %d", len(page.Messages), page.Messages[0].Content, page.HasMore, page.NextOffset)
	}
}

// maxChars cuts each message's text by characters, not bytes, and marks the cut
// when there is room; the stored transcript is left alone.
func TestPageHistoryMaxChars(t *testing.T) {
	h := []DeviceHistoryMessage{{Role: "assistant", Content: "héllo wörld, this is long"}}
	page, err := pageHistory(h, historyPageParams{MaxChars: new(8)})
	if err != nil {
		t.Fatal(err)
	}
	if got := page.Messages[0].Content; got != "héllo..." {
		t.Errorf("truncated = %q, want %q", got, "héllo...")
	}
	if h[0].Content != "héllo wörld, this is long" {
		t.Errorf("source transcript was modified: %q", h[0].Content)
	}
	for _, tt := range []struct {
		in, want string
		n        int
	}{
		{"abcdef", "abc", 3},     // no room for the marker
		{"abcdef", "a...", 4},    // marker fits
		{"abc", "abc", 3},        // exact length is not cut
		{"ééééééé", "ééé...", 6}, // multi-byte characters count as one
	} {
		if got := truncateChars(tt.in, tt.n); got != tt.want {
			t.Errorf("truncateChars(%q, %d) = %q, want %q", tt.in, tt.n, got, tt.want)
		}
	}
}

// historyQuerier serves a fixed transcript for every session key.
type historyQuerier struct {
	recordingQuerier
	msgs []DeviceHistoryMessage
}

func (q *historyQuerier) History(string) []DeviceHistoryMessage { return q.msgs }

// Over the wire, the paging fields are read from the chat.history params and
// the page facts are returned beside the messages; a bad value is refused as
// INVALID_REQUEST.
func TestChatHistoryPagingOverWire(t *testing.T) {
	srv, _, wsURL := newTestServer(t, ServerOptions{ServerVersion: "test-1", AutoApprove: true})
	srv.SetQuerier(&historyQuerier{msgs: transcript(5)})
	em := newEmulator(t)
	conn := mustOpen(t, em, wsURL, "")

	em.writeReq(t, conn, "h1", "chat.history", map[string]any{
		"sessionKey": "agent:bob:main", "limit": 2, "offset": 1, "maxChars": 1,
	})
	r := readRes(t, conn, "h1")
	if !r.OK {
		t.Fatalf("chat.history refused: %+v", r.Error)
	}
	var got struct {
		Messages []struct {
			Content []struct {
				Text string `json:"text"`
			} `json:"content"`
		} `json:"messages"`
		Offset        int  `json:"offset"`
		TotalMessages int  `json:"totalMessages"`
		HasMore       bool `json:"hasMore"`
		NextOffset    int  `json:"nextOffset"`
	}
	if err := json.Unmarshal(r.Payload, &got); err != nil {
		t.Fatalf("decode payload: %v", err)
	}
	if len(got.Messages) != 2 || got.Messages[0].Content[0].Text != "3" || got.Messages[1].Content[0].Text != "4" {
		t.Fatalf("messages = %+v, want texts 3 and 4", got.Messages)
	}
	if got.Offset != 1 || got.TotalMessages != 5 || !got.HasMore || got.NextOffset != 3 {
		t.Fatalf("offset/total/hasMore/next = %d/%d/%v/%d, want 1/5/true/3", got.Offset, got.TotalMessages, got.HasMore, got.NextOffset)
	}

	em.writeReq(t, conn, "h2", "chat.history", map[string]any{"sessionKey": "agent:bob:main", "limit": 0})
	if r := readRes(t, conn, "h2"); r.OK || r.Error == nil || r.Error.Code != gatewayproto.CodeInvalidRequest {
		t.Fatalf("limit 0 accepted or wrong error: ok=%v err=%+v", r.OK, r.Error)
	}
}
