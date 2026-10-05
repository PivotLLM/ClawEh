package device

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"github.com/PivotLLM/ClawEh/gatewayproto"
)

const (
	fuzzSharedToken = "fuzz-shared-token-0123456789"
	fuzzWordToken   = "alpha-bravo-charlie-delta-echo"
	fuzzNonce       = "fuzz-nonce-16byte"
	fuzzConnID      = "fuzz-conn"
)

// fuzzDeviceKey is a fixed Ed25519 identity so seeds mean the same thing in every
// fuzz worker process.
func fuzzDeviceKey() (ed25519.PublicKey, ed25519.PrivateKey) {
	seed := []byte("device-handshake-fuzz-seed-01234") // 32 bytes
	priv := ed25519.NewKeyFromSeed(seed[:ed25519.SeedSize])
	pub, ok := priv.Public().(ed25519.PublicKey)
	if !ok {
		panic("ed25519 private key did not yield an ed25519 public key")
	}
	return pub, priv
}

// newFuzzServer opens a pairing store in a temp dir and builds a Server with both
// shared secrets configured so the fuzzer can reach every branch of the handshake.
func newFuzzServer(f *testing.F) *Server {
	f.Helper()
	store, err := OpenStore(context.Background(), filepath.Join(f.TempDir(), "gateway.db"))
	if err != nil {
		f.Fatal(err)
	}
	f.Cleanup(func() {
		if err := store.Close(); err != nil {
			f.Errorf("Close: %v", err)
		}
	})
	return NewServer(store, ServerOptions{
		SharedToken: fuzzSharedToken, WordToken: fuzzWordToken,
		ServerVersion: "fuzz", AutoApprove: true,
	})
}

// connectFrame builds a connect request frame with the given params object.
func connectFrame(id string, params string) string {
	return `{"type":"req","id":"` + id + `","method":"connect","params":` + params + `}`
}

// signedConnectParams returns connect params carrying a genuine device identity
// signed for fuzzNonce at signedAt (the seed uses a fixed, stale timestamp: the
// fuzzer cannot forge a live one, and the live path is covered by
// FuzzVerifyDeviceIdentity).
func signedConnectParams(token string, signedAt int64) string {
	pub, priv := fuzzDeviceKey()
	id := gatewayproto.DeviceIDFromPublicKey(pub)
	in := gatewayproto.DeviceAuthInput{
		DeviceID: id, ClientID: "rabbit-r1", ClientMode: gatewayproto.ModeNode, Role: gatewayproto.RoleNode,
		SignedAtMs: signedAt, Token: token, Nonce: fuzzNonce, Platform: "rabbit", DeviceFamily: "r1",
	}
	sig := ed25519.Sign(priv, []byte(gatewayproto.BuildDeviceAuthPayloadV3(in)))
	b64 := base64.RawURLEncoding.EncodeToString
	p := gatewayproto.ConnectParams{
		MinProtocol: 3, MaxProtocol: 4,
		Client: gatewayproto.ClientInfo{ID: "rabbit-r1", Version: "1", Platform: "rabbit", DeviceFamily: "r1", Mode: gatewayproto.ModeNode},
		Role:   gatewayproto.RoleNode,
		Device: &gatewayproto.DeviceIdentity{ID: id, PublicKey: b64(pub), Signature: b64(sig), SignedAt: signedAt, Nonce: fuzzNonce},
		Auth:   &gatewayproto.ConnectAuth{Token: token},
	}
	raw, err := json.Marshal(p)
	if err != nil {
		panic(err)
	}
	return string(raw)
}

// validCloseCodes are the websocket close codes the handshake is allowed to use.
var validCloseCodes = map[int]bool{
	websocket.ClosePolicyViolation:   true,
	websocket.CloseProtocolError:     true,
	websocket.CloseInternalServerErr: true,
	websocket.CloseMessageTooBig:     true,
}

// FuzzHandshake feeds raw first frames to Server.handshake: it must never panic,
// must return exactly one of hello/fail, and a failure must be a complete,
// encodable error response correlated to the request id.
func FuzzHandshake(f *testing.F) {
	srv := newFuzzServer(f)

	f.Add([]byte(connectFrame("1", signedConnectParams(fuzzSharedToken, 1700000000000))))
	f.Add([]byte(connectFrame("2", signedConnectParams(fuzzWordToken, time.Now().UnixMilli()))))
	f.Add([]byte(connectFrame("3", signedConnectParams("wrong-token", 1700000000000))))
	f.Add([]byte(connectFrame("4", `{"minProtocol":3,"maxProtocol":4,"client":{"id":"c","mode":"node"},"auth":{"token":"`+fuzzSharedToken+`"}}`)))
	f.Add([]byte(connectFrame("5", `{"minProtocol":3,"maxProtocol":4,"client":{"id":"c","mode":"node"},"auth":{"deviceToken":"issued"},"device":{"id":"x","publicKey":"!!","signature":"","signedAt":0,"nonce":"`+fuzzNonce+`"}}`)))
	f.Add([]byte(connectFrame("6", `{"minProtocol":1,"maxProtocol":2,"client":{"id":"c","mode":"probe"}}`)))
	f.Add([]byte(connectFrame("7", `{"minProtocol":5,"maxProtocol":9}`)))
	f.Add([]byte(connectFrame("8", `"string"`)))
	f.Add([]byte(connectFrame("9", `null`)))
	f.Add([]byte(`{"type":"req","id":"10","method":"chat.send","params":{"message":"hi"}}`))
	f.Add([]byte(`{"type":"event","event":"tick"}`))
	f.Add([]byte(`{"type":"res","id":"11","ok":true}`))
	f.Add([]byte(`{}`))
	f.Add([]byte(``))
	f.Add([]byte(`garbage`))
	f.Add([]byte("\xff\xfe"))

	f.Fuzz(func(t *testing.T, raw []byte) {
		// Fresh throttle per input: repeated auth failures would otherwise lock
		// the fixed test IP out and short-circuit every later input.
		srv.throttle = newAuthThrottle()
		r := httptest.NewRequest(http.MethodGet, "/", nil)

		hello, fail := srv.handshake(r, fuzzConnID, fuzzNonce, raw)
		if (hello == nil) == (fail == nil) {
			t.Fatalf("handshake(%q): want exactly one of hello/fail, got %v / %v", raw, hello, fail)
		}

		var req gatewayproto.RequestFrame
		reqDecoded := json.Unmarshal(raw, &req) == nil && req.Type == gatewayproto.FrameReq

		if fail != nil {
			if fail.err == nil || fail.err.Code == "" || fail.err.Message == "" || fail.reason == "" {
				t.Fatalf("handshake(%q): incomplete failure %+v", raw, fail)
			}
			if !validCloseCodes[fail.code] {
				t.Fatalf("handshake(%q): unexpected close code %d", raw, fail.code)
			}
			if reqDecoded && fail.id != req.ID {
				t.Fatalf("handshake(%q): failure id %q does not match request id %q", raw, fail.id, req.ID)
			}
			if _, err := json.Marshal(gatewayproto.NewErrorResponse(fail.id, fail.err)); err != nil {
				t.Fatalf("handshake(%q): error response not encodable: %v", raw, err)
			}
			return
		}

		if !reqDecoded || req.Method != "connect" { //nolint:usestdlibvars // protocol method name
			t.Fatalf("handshake(%q) succeeded for a frame that is not a connect request", raw)
		}
		if hello.id != req.ID || hello.deviceID == "" || hello.chatID != "device:"+hello.deviceID {
			t.Fatalf("handshake(%q): inconsistent hello %+v", raw, hello)
		}
		if hello.payload.Type != "hello-ok" || hello.payload.Protocol == 0 {
			t.Fatalf("handshake(%q): malformed hello-ok %+v", raw, hello.payload)
		}
		if _, err := json.Marshal(gatewayproto.NewOKResponse(hello.id, hello.payload)); err != nil {
			t.Fatalf("handshake(%q): hello-ok not encodable: %v", raw, err)
		}
	})
}

// FuzzVerifyDeviceIdentity builds connect params from fuzzed fields, signs them
// live with the fixed device key, and checks that verifyDeviceIdentity accepts
// exactly a genuine, fresh signature and rejects a nonce or identity tamper.
func FuzzVerifyDeviceIdentity(f *testing.F) {
	srv := newFuzzServer(f)

	f.Add("rabbit-r1", "node", "node", "operator.read", fuzzSharedToken, "rabbit", "r1", int64(0))
	f.Add("", "", "", "", "", "", "", int64(0))
	f.Add("c", "ui", "operator", "a,b", "", "Android", "Pixel", int64(-signatureSkewMs+30_000))
	f.Add("c", "node", "node", "", "t", "p", "f", int64(signatureSkewMs+60_000))
	f.Add("c", "node", "node", "", "t", "p", "f", int64(-1<<62))
	f.Add("c|x", "node", "node", "s|t", "t|u", "p|q", "f|g", int64(0))

	f.Fuzz(func(t *testing.T, clientID, mode, role, scopesCSV, token, platform, family string, skewMs int64) {
		pub, priv := fuzzDeviceKey()
		id := gatewayproto.DeviceIDFromPublicKey(pub)
		var scopes []string
		if scopesCSV != "" {
			scopes = strings.Split(scopesCSV, ",")
		}
		effRole := role
		if effRole == "" {
			effRole = gatewayproto.RoleNode
		}
		now := time.Now().UnixMilli()
		signedAt := now + skewMs
		in := gatewayproto.DeviceAuthInput{
			DeviceID: id, ClientID: clientID, ClientMode: mode, Role: effRole, Scopes: scopes,
			SignedAtMs: signedAt, Token: token, Nonce: fuzzNonce, Platform: platform, DeviceFamily: family,
		}
		sig := ed25519.Sign(priv, []byte(gatewayproto.BuildDeviceAuthPayloadV3(in)))
		b64 := base64.RawURLEncoding.EncodeToString
		var auth *gatewayproto.ConnectAuth
		if token != "" {
			auth = &gatewayproto.ConnectAuth{Token: token}
		}
		p := gatewayproto.ConnectParams{
			Client: gatewayproto.ClientInfo{ID: clientID, Mode: mode, Platform: platform, DeviceFamily: family},
			Role:   role, Scopes: scopes, Auth: auth,
			Device: &gatewayproto.DeviceIdentity{ID: id, PublicKey: b64(pub), Signature: b64(sig), SignedAt: signedAt, Nonce: fuzzNonce},
		}

		fail := srv.verifyDeviceIdentity("req", fuzzNonce, &p)
		// Leave a 2s band around the skew limit unasserted: time.Now moves between
		// our reading and the server's.
		const band = 2000
		switch {
		case skewMs > -(signatureSkewMs-band) && skewMs < signatureSkewMs-band:
			if fail != nil {
				t.Fatalf("genuine fresh identity rejected: %s (params %+v)", fail.err.Message, p)
			}
		case skewMs < -(signatureSkewMs+band) || skewMs > signatureSkewMs+band:
			if fail == nil {
				t.Fatalf("stale identity (skew %dms) accepted", skewMs)
			}
		}
		if fail != nil && fail.id != "req" {
			t.Fatalf("failure id %q, want %q", fail.id, "req")
		}

		// Tamper cases must fail regardless of freshness.
		if got := srv.verifyDeviceIdentity("req", fuzzNonce+"x", &p); got == nil {
			t.Fatal("nonce mismatch accepted")
		}
		tampered := p
		dev := *p.Device
		dev.ID = strings.ToUpper(id[:1]) + id[1:]
		if dev.ID == id {
			dev.ID = id[:len(id)-1] + "x"
		}
		tampered.Device = &dev
		if got := srv.verifyDeviceIdentity("req", fuzzNonce, &tampered); got == nil {
			t.Fatal("device id / public key mismatch accepted")
		}
		tampered = p
		dev = *p.Device
		dev.Nonce = fuzzNonce + "x"
		tampered.Device = &dev
		if got := srv.verifyDeviceIdentity("req", fuzzNonce, &tampered); got == nil {
			t.Fatal("device nonce not matching challenge accepted")
		}
	})
}

// FuzzParseSlashCommand: ok exactly when the trimmed message starts with "/",
// cmd is lowercase and whitespace-free, arg is trimmed.
func FuzzParseSlashCommand(f *testing.F) {
	for _, s := range []string{"/agent bob", "/AGENT", "  /help  ", "/", "/ x", "hello", "", "/agent\tbob  x", "//a", "/a b", "/ünïcode Ärg"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, message string) {
		cmd, arg, ok := parseSlashCommand(message)
		trimmed := strings.TrimSpace(message)
		if ok != strings.HasPrefix(trimmed, "/") {
			t.Fatalf("parseSlashCommand(%q) ok=%v, want %v", message, ok, !ok)
		}
		if !ok {
			if cmd != "" || arg != "" {
				t.Fatalf("parseSlashCommand(%q) returned %q/%q with ok=false", message, cmd, arg)
			}
			return
		}
		if cmd != strings.ToLower(cmd) || strings.ContainsAny(cmd, " \t") {
			t.Fatalf("parseSlashCommand(%q) cmd %q not normalized", message, cmd)
		}
		if arg != strings.TrimSpace(arg) {
			t.Fatalf("parseSlashCommand(%q) arg %q not trimmed", message, arg)
		}
	})
}
