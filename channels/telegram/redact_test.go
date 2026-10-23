package telegram

import (
	"bytes"
	"context"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	ta "github.com/mymmrac/telego/telegoapi"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/PivotLLM/ClawEh/bus"
	"github.com/PivotLLM/ClawEh/channels"
	"github.com/PivotLLM/ClawEh/config"
	"github.com/PivotLLM/ClawEh/logger"
)

// testSecret is the part of testToken that must never be written out.
var testSecret = strings.SplitN(testToken, ":", 2)[1]

func TestRedactToken(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{
			name: "poll URL in a transport error",
			in:   `Post "https://api.telegram.org/bot` + testToken + `/getUpdates": http2: timeout awaiting response headers`,
			want: `Post "https://api.telegram.org/bot<redacted>/getUpdates": http2: timeout awaiting response headers`,
		},
		{
			name: "custom API server",
			in:   "http://localhost:8081/bot" + testToken + "/sendMessage",
			want: "http://localhost:8081/bot<redacted>/sendMessage",
		},
		{
			name: "file download URL",
			in:   "https://api.telegram.org/file/bot" + testToken + "/voice/file_3.oga",
			want: "https://api.telegram.org/file/bot<redacted>/voice/file_3.oga",
		},
		{
			name: "no token",
			in:   "dial tcp: i/o timeout",
			want: "dial tcp: i/o timeout",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, RedactToken(tt.in))
		})
	}
}

// A redacted error keeps its chain, so it is still classified correctly.
func TestRedactErr(t *testing.T) {
	require.NoError(t, redactErr(nil))

	plain := errors.New("dial tcp: i/o timeout")
	require.Same(t, plain, redactErr(plain), "an error without a token is returned as is")

	orig := fmt.Errorf("request to %s: %w", "https://api.telegram.org/bot"+testToken+"/getUpdates", channels.ErrTemporary)
	red := redactErr(orig)
	assert.NotContains(t, red.Error(), testSecret)
	assert.Contains(t, red.Error(), "bot<redacted>/getUpdates")
	assert.ErrorIs(t, red, channels.ErrTemporary)
}

// tokenURLError is the error net/http returns for a failed request to a
// Telegram API URL: its text carries the bot token.
func tokenURLError(method string) error {
	return &url.Error{
		Op:  "Post",
		URL: "https://api.telegram.org/bot" + testToken + "/" + method,
		Err: errors.New("dial tcp: i/o timeout"),
	}
}

// Every message telego logs through the adapter has the token removed.
func TestTelegoLogger_RedactsToken(t *testing.T) {
	var buf bytes.Buffer
	restore := logger.RedirectForTest(&buf)
	defer restore()

	l := telegoLogger{logger.NewLogger("telego")}
	l.Errorf("Execution error %s: %s", "getUpdates", "request call: http do request: "+tokenURLError("getUpdates").Error())
	l.Debugf("API call to: %q", "https://api.telegram.org/bot"+testToken+"/getMe")

	out := buf.String()
	assert.NotContains(t, out, testSecret)
	assert.Equal(t, 2, strings.Count(out, "bot<redacted>"), out)
}

// A failed send logs and returns no token.
func TestSend_FailureRedactsToken(t *testing.T) {
	var buf bytes.Buffer
	restore := logger.RedirectForTest(&buf)
	defer restore()

	caller := &stubCaller{
		callFn: func(context.Context, string, *ta.RequestData) (*ta.Response, error) {
			return nil, tokenURLError("sendMessage")
		},
	}
	ch := newTestChannel(t, caller)
	ch.placeholderCfg = config.PlaceholderConfig{Enabled: true}

	sendErr := ch.Send(context.Background(), bus.OutboundMessage{ChatID: "12345", Content: "hello"})
	require.Error(t, sendErr)
	editErr := ch.EditMessage(context.Background(), "12345", "1", "hello")
	require.Error(t, editErr)
	_, phErr := ch.SendPlaceholder(context.Background(), "12345")
	require.Error(t, phErr)

	for _, err := range []error{sendErr, editErr, phErr} {
		assert.NotContains(t, err.Error(), testSecret)
	}
	assert.Contains(t, editErr.Error(), "bot<redacted>")
	assert.Contains(t, sendErr.Error(), "bot<redacted>", "the send error keeps its (redacted) cause for the manager's log and alert")
	assert.NotContains(t, buf.String(), testSecret)
}

// A failed poll logs no token and hands the connection tracker (whose last
// error becomes the "Telegram down" alert details) none either.
func TestPollFailed_RedactsToken(t *testing.T) {
	var buf bytes.Buffer
	restore := logger.RedirectForTest(&buf)
	defer restore()

	ch := newTestChannel(t, &stubCaller{})
	rec := &alertRecorder{}
	ch.SetAlerter(rec)
	ch.pollFailed(tokenURLError("getUpdates"), 0)
	ch.pollFailed(&ta.Error{ErrorCode: 401, Description: "Unauthorized " + "bot" + testToken}, 0)

	assert.Contains(t, buf.String(), "bot<redacted>")
	assert.NotContains(t, buf.String(), testSecret)
	require.Len(t, rec.alerts, 1)
	assert.NotContains(t, rec.alerts[0].Description, testSecret)
	ch.SetRunning(false)
}

// The transport never negotiates HTTP/2, even against a server that offers it
// and after the process default transport (which the Telegram transport is
// cloned from) has been used and so advertises h2 in its ALPN list.
func TestNewHTTPTransport_HTTP1Only(t *testing.T) {
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, err := io.WriteString(w, r.Proto); err != nil {
			t.Error(err)
		}
	}))
	srv.EnableHTTP2 = true
	srv.StartTLS()
	defer srv.Close()

	// Use the default transport once so it stamps h2 into its TLS config, the
	// state the production process is in by the time a bot connects. The
	// request itself fails on the untrusted test certificate; that is fine.
	if resp, err := http.DefaultClient.Get(srv.URL); err == nil {
		require.NoError(t, resp.Body.Close())
	}

	tr, err := newHTTPTransport("")
	require.NoError(t, err)
	assert.False(t, tr.ForceAttemptHTTP2)
	require.NotNil(t, tr.Protocols)
	assert.True(t, tr.Protocols.HTTP1())
	assert.False(t, tr.Protocols.HTTP2())
	assert.Equal(t, []string{"http/1.1"}, tr.TLSClientConfig.NextProtos)
	assert.Positive(t, tr.ResponseHeaderTimeout)

	pool := x509.NewCertPool()
	pool.AddCert(srv.Certificate())
	tr.TLSClientConfig.RootCAs = pool
	client := &http.Client{Transport: tr}
	resp, err := client.Get(srv.URL)
	require.NoError(t, err)
	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	require.NoError(t, resp.Body.Close())
	assert.Equal(t, "HTTP/1.1", string(body))
	assert.Equal(t, "HTTP/1.1", resp.Proto)
}
