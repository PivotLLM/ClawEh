package secmsg

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/PivotLLM/ClawEh/pkg/bus"
	"github.com/PivotLLM/ClawEh/pkg/channels"

	smclient "github.com/tenebris-tech/secmsg/client"
	"github.com/tenebris-tech/secmsg/schema"
)

// TestLive exercises the real connect/handshake/subscribe path against a running
// daemon. It is read-only (no sends) and gated on SECMSG_LIVE_ADDR, e.g.:
//
//	SECMSG_LIVE_ADDR=127.0.0.1:9801 SECMSG_LIVE_ACCOUNT=droid1 \
//	  go test ./pkg/channels/secmsg -run TestLive -v
func TestLive(t *testing.T) {
	addr := os.Getenv("SECMSG_LIVE_ADDR")
	if addr == "" {
		t.Skip("set SECMSG_LIVE_ADDR to run the live daemon test")
	}
	account := os.Getenv("SECMSG_LIVE_ACCOUNT") // may be "" to exercise auto-select

	cl, err := smclient.Dial(addr, smclient.WithTimeout(10*time.Second))
	if err != nil {
		t.Fatalf("dial %s: %v", addr, err)
	}
	defer cl.Close()

	info := cl.Info()
	t.Logf("handshake: service=%q daemon=%q version=%q link=%q accounts=%d caps=%v",
		info.Service, info.Daemon, info.Version, info.LinkMethod, info.Accounts, info.Capabilities)
	if info.Service == "" {
		t.Fatalf("daemon advertised no service in handshake")
	}

	ch := &SecMsgChannel{
		BaseChannel: channels.NewBaseChannel("signal", nil, bus.NewMessageBus(), []string{"*"}),
		wantAccount: account,
		ctx:         context.Background(),
	}

	resolved, err := ch.resolveAccount(cl, info.Service)
	if account != "" {
		if err != nil {
			t.Fatalf("resolveAccount(%q): %v", account, err)
		}
		if resolved != account {
			t.Fatalf("resolveAccount = %q, want %q", resolved, account)
		}
		t.Logf("resolved account: %q", resolved)
	} else {
		// A daemon with several accounts must refuse to guess.
		t.Logf("auto-select result: resolved=%q err=%v", resolved, err)
	}

	if resolved == "" {
		return
	}

	// Subscribe and passively observe for a few seconds. We never send; this only
	// confirms the subscription is accepted and surfaces the shape of any inbound
	// traffic that happens to arrive (useful to confirm To.Self group detection).
	subCtx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()
	events, unsub, err := cl.Subscribe(subCtx, resolved)
	if err != nil {
		t.Fatalf("subscribe %q: %v", resolved, err)
	}
	defer unsub()

	for {
		select {
		case <-subCtx.Done():
			t.Log("subscription held for the observation window (no errors)")
			return
		case env, ok := <-events:
			if !ok {
				t.Fatal("subscription closed unexpectedly")
			}
			if env != nil && env.Method == schema.MethodMessage {
				t.Logf("observed inbound message envelope: %s", string(env.Params))
			}
		}
	}
}
