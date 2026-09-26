package line

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/PivotLLM/ClawEh/config"
)

const fuzzChannelSecret = "fuzz-line-channel-secret"

// lineSignature computes the X-Line-Signature LINE would send for body.
func lineSignature(secret string, body []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	return base64.StdEncoding.EncodeToString(mac.Sum(nil))
}

// webhookBodySeeds are representative LINE webhook payloads.
var webhookBodySeeds = []string{
	`{"events":[]}`,
	`{"destination":"U1","events":[{"type":"message","replyToken":"r","timestamp":1700000000000,"source":{"type":"user","userId":"U2"},"message":{"id":"1","type":"text","text":"hello","quoteToken":"q"}}]}`,
	`{"events":[{"type":"message","replyToken":"r","source":{"type":"group","groupId":"G1","userId":"U2"},"message":{"id":"2","type":"text","text":"@Bot hi there","mention":{"mentionees":[{"index":0,"length":4,"type":"user","userId":"Ubot"}]}}}]}`,
	`{"events":[{"type":"message","source":{"type":"room","roomId":"R1"},"message":{"id":"3","type":"text","text":"héllo @Bot wörld","mention":{"mentionees":[{"index":6,"length":4,"type":"all"}]}}}]}`,
	`{"events":[{"type":"message","source":{"type":"group","groupId":"G1"},"message":{"id":"4","type":"text","text":"x","mention":{"mentionees":[{"index":5,"length":10,"type":"user","userId":"Ubot"}]}}}]}`,
	`{"events":[{"type":"message","source":{"type":"user","userId":"U2"},"message":{"id":"5","type":"image","contentProvider":{"type":"line"}}}]}`,
	`{"events":[{"type":"message","source":{"type":"user","userId":"U2"},"message":{"id":"6","type":"sticker"}}]}`,
	`{"events":[{"type":"follow","source":{"type":"user","userId":"U2"}}]}`,
	`{"events":[{"type":"message","message":"not-an-object"}]}`,
	`{"events":[{"type":"message","message":null}]}`,
	`{"events":null}`,
	`{"events":"x"}`,
	`{}`,
	`[]`,
	``,
	`{"events":[{"type":"message","source":{"type":"group","groupId":"G1"},"message":{"id":"7","type":"text","text":"abc","mention":{"mentionees":[{"index":-1,"length":2,"type":"user","userId":"Ubot"}]}}}]}`,
}

// FuzzVerifySignature: the HMAC check never panics and accepts exactly the
// genuine base64 signature for (secret, body).
func FuzzVerifySignature(f *testing.F) {
	for _, body := range webhookBodySeeds {
		f.Add(fuzzChannelSecret, []byte(body), lineSignature(fuzzChannelSecret, []byte(body)))
		f.Add(fuzzChannelSecret, []byte(body), "invalidsignature")
	}
	f.Add("", []byte(`{}`), lineSignature("", []byte(`{}`)))
	f.Add(fuzzChannelSecret, []byte{}, "")
	f.Add(fuzzChannelSecret, []byte(`{}`), "\x00")
	f.Add(fuzzChannelSecret, []byte(`{}`), strings.Repeat("A", 100_000))
	f.Fuzz(func(t *testing.T, secret string, body []byte, sig string) {
		ch := &LINEChannel{config: config.LINEConfig{ChannelSecret: secret}}
		got := ch.verifySignature(body, sig)
		want := sig != "" && sig == lineSignature(secret, body)
		if got != want {
			t.Fatalf("verifySignature(secret=%q, body=%q, sig=%q) = %v, want %v", secret, body, sig, got, want)
		}
	})
}

// FuzzWebhookHandler drives the HTTP handler with a body and a signature header.
// Without the genuine signature the handler must answer 413 (oversized) or 403
// and never panic; it must not reach payload parsing. A body that happens to
// carry its genuine signature is skipped: that path dispatches events to the
// LINE API asynchronously and is covered by FuzzWebhookPayload instead.
func FuzzWebhookHandler(f *testing.F) {
	for _, body := range webhookBodySeeds {
		f.Add([]byte(body), "invalidsignature")
		f.Add([]byte(body), "")
	}
	f.Add([]byte(`{}`), lineSignature("other-secret", []byte(`{}`)))
	f.Add(bytes.Repeat([]byte("A"), maxWebhookBodySize+1), "x")
	f.Fuzz(func(t *testing.T, body []byte, sig string) {
		if sig == lineSignature(fuzzChannelSecret, body) {
			t.Skip("genuine signature; payload path is fuzzed separately")
		}
		ch := &LINEChannel{config: config.LINEConfig{ChannelSecret: fuzzChannelSecret}}
		req := httptest.NewRequest(http.MethodPost, "/webhook/line", bytes.NewReader(body))
		if sig != "" {
			// Header values cannot carry arbitrary bytes; only set what a client could send.
			if strings.ContainsAny(sig, "\r\n\x00") || !utf8.ValidString(sig) {
				t.Skip("signature not representable as a header value")
			}
			req.Header.Set("X-Line-Signature", sig)
		}
		rec := httptest.NewRecorder()
		ch.webhookHandler(rec, req)

		want := http.StatusForbidden
		if len(body) > maxWebhookBodySize {
			want = http.StatusRequestEntityTooLarge
		}
		if rec.Code != want {
			t.Fatalf("webhookHandler(body=%d bytes, sig=%q) = %d, want %d", len(body), sig, rec.Code, want)
		}
	})
}

// FuzzWebhookPayload exercises the post-signature parsing path on a body the
// handler would accept: the events envelope, each event's message, chat-id
// resolution and the group mention logic. Mention indices come straight from
// the webhook body, so the slicing they drive must never panic, and stripping
// a mention must never make the text longer or leave surrounding whitespace.
func FuzzWebhookPayload(f *testing.F) {
	for _, body := range webhookBodySeeds {
		f.Add([]byte(body), "Ubot", "Bot")
	}
	f.Add([]byte(webhookBodySeeds[2]), "", "")
	f.Add([]byte(webhookBodySeeds[3]), "Ubot", "")
	f.Fuzz(func(t *testing.T, body []byte, botUserID, botDisplayName string) {
		ch := &LINEChannel{
			config:         config.LINEConfig{ChannelSecret: fuzzChannelSecret},
			botUserID:      botUserID,
			botDisplayName: botDisplayName,
		}
		var payload struct {
			Events []lineEvent `json:"events"`
		}
		if err := json.Unmarshal(body, &payload); err != nil {
			return // the handler answers 400 here
		}
		for _, ev := range payload.Events {
			_ = ch.resolveChatID(ev.Source)
			if ev.Type != "message" {
				continue
			}
			var msg lineMessage
			if err := json.Unmarshal(ev.Message, &msg); err != nil {
				continue
			}
			_ = ch.isBotMentioned(msg)
			stripped := ch.stripBotMention(msg.Text, msg)
			if stripped != strings.TrimSpace(stripped) {
				t.Fatalf("stripBotMention(%q) = %q: not trimmed", msg.Text, stripped)
			}
			if utf8.RuneCountInString(stripped) > utf8.RuneCountInString(msg.Text) {
				t.Fatalf("stripBotMention(%q) = %q: longer than input", msg.Text, stripped)
			}
		}
	})
}
