package gatewayproto

import (
	"bytes"
	"encoding/json"
	"reflect"
	"testing"
)

// frameSeeds are representative inbound frames: the connect request a Rabbit
// R1 sends, a chat.send, malformed and boundary cases. Every fuzz target over
// raw frames starts from these.
var frameSeeds = []string{
	`{"type":"req","id":"1","method":"connect","params":{"minProtocol":3,"maxProtocol":4,"client":{"id":"rabbit-r1","version":"1.0","platform":"rabbit","deviceFamily":"r1","mode":"node"},"role":"node","scopes":["operator.read"],"device":{"id":"abc","publicKey":"AAAA","signature":"BBBB","signedAt":1700000000000,"nonce":"n"},"auth":{"token":"tok"}}}`,
	`{"type":"req","id":"2","method":"chat.send","params":{"message":"hello","sessionKey":"main","idempotencyKey":"run-1","attachments":[{"mimeType":"image/jpeg","name":"a.jpg","data":"AAECAw=="}]}}`,
	`{"type":"req","id":"3","method":"health"}`,
	`{"type":"req","id":"4","method":"node.event","params":{"event":"x","payload":null}}`,
	`{"type":"res","id":"5","ok":true,"payload":{"runId":"r","status":"started"}}`,
	`{"type":"event","event":"tick","seq":7}`,
	`{"type":""}`,
	`{}`,
	`[]`,
	`null`,
	``,
	`{"type":"req","id":1,"method":"connect"}`,
	`{"type":"req","id":"6","method":"connect","params":"not-an-object"}`,
	`{"type":"req","id":"7","method":"connect","params":{"minProtocol":"3"}}`,
	`{"type":"req","id":"8","method":"connect","params":{"device":null,"auth":null,"scopes":null}}`,
	`{"type":"req","id":"9","method":"connect","params":{"minProtocol":-1,"maxProtocol":9223372036854775807}}`,
	"{\"type\":\"req\",\"id\":\"\\u0000\",\"method\":\"\xff\"}",
}

// FuzzFrameKind: FrameKind never panics, and returns a non-empty kind exactly
// when the frame is a JSON object with a non-empty string "type".
func FuzzFrameKind(f *testing.F) {
	for _, s := range frameSeeds {
		f.Add([]byte(s))
	}
	f.Fuzz(func(t *testing.T, raw []byte) {
		kind, err := FrameKind(raw)
		if (kind == "") == (err == nil) {
			t.Fatalf("FrameKind(%q) = (%q, %v): want exactly one of kind or err", raw, kind, err)
		}
		if err != nil {
			return
		}
		var head struct {
			Type string `json:"type"`
		}
		if uerr := json.Unmarshal(raw, &head); uerr != nil || head.Type != kind {
			t.Fatalf("FrameKind(%q) = %q, but independent decode gives (%q, %v)", raw, kind, head.Type, uerr)
		}
	})
}

// FuzzRequestFrameRoundTrip: a RequestFrame that decodes must re-encode and
// decode again to an equal value (Params compared as canonical JSON).
func FuzzRequestFrameRoundTrip(f *testing.F) {
	for _, s := range frameSeeds {
		f.Add([]byte(s))
	}
	f.Fuzz(func(t *testing.T, raw []byte) {
		var req RequestFrame
		if err := json.Unmarshal(raw, &req); err != nil {
			return
		}
		if len(req.Params) > 0 && !json.Valid(req.Params) {
			t.Fatalf("decoded Params is not valid JSON: %q", req.Params)
		}
		enc, err := json.Marshal(req)
		if err != nil {
			t.Fatalf("re-encode decoded frame %q: %v", raw, err)
		}
		var again RequestFrame
		if err := json.Unmarshal(enc, &again); err != nil {
			t.Fatalf("decode re-encoded frame %q: %v", enc, err)
		}
		if req.Type != again.Type || req.ID != again.ID || req.Method != again.Method {
			t.Fatalf("round trip changed header: %+v -> %+v", req, again)
		}
		if !equalJSON(t, req.Params, again.Params) {
			t.Fatalf("round trip changed params: %q -> %q", req.Params, again.Params)
		}
	})
}

// FuzzConnectParamsRoundTrip: ConnectParams that decode must re-encode and
// decode to a deeply equal value, and the canonical auth payload derived from
// them must be stable across the round trip.
func FuzzConnectParamsRoundTrip(f *testing.F) {
	for _, s := range frameSeeds {
		var req RequestFrame
		if json.Unmarshal([]byte(s), &req) == nil && len(req.Params) > 0 {
			f.Add([]byte(req.Params))
		}
	}
	f.Add([]byte(`{"minProtocol":3,"maxProtocol":4,"client":{"id":"c","mode":"node"},"permissions":{"a":true},"caps":["x"],"commands":["y"],"locale":"en","userAgent":"ua"}`))
	f.Fuzz(func(t *testing.T, raw []byte) {
		var p ConnectParams
		if err := json.Unmarshal(raw, &p); err != nil {
			return
		}
		enc, err := json.Marshal(p)
		if err != nil {
			t.Fatalf("re-encode ConnectParams from %q: %v", raw, err)
		}
		var again ConnectParams
		if err = json.Unmarshal(enc, &again); err != nil {
			t.Fatalf("decode re-encoded ConnectParams %q: %v", enc, err)
		}
		// Encoding must be a fixed point: encode(decode(encode(p))) == encode(p).
		// (DeepEqual is deliberately not used: omitempty turns an empty slice
		// into nil across a round trip, which is standard encoding/json behaviour.)
		enc2, err := json.Marshal(again)
		if err != nil {
			t.Fatalf("re-encode round-tripped ConnectParams: %v", err)
		}
		if !bytes.Equal(enc, enc2) {
			t.Fatalf("ConnectParams encoding not stable:\n%s\n%s", enc, enc2)
		}
		in := authInputFromParams(p)
		if BuildDeviceAuthPayloadV3(in) != BuildDeviceAuthPayloadV3(authInputFromParams(again)) {
			t.Fatal("v3 auth payload not stable across round trip")
		}
		if BuildDeviceAuthPayloadV2(in) != BuildDeviceAuthPayloadV2(authInputFromParams(again)) {
			t.Fatal("v2 auth payload not stable across round trip")
		}
	})
}

// FuzzNegotiateProtocol: the negotiated version is 0 or lies inside both ranges.
func FuzzNegotiateProtocol(f *testing.F) {
	f.Add(3, 4, false)
	f.Add(4, 4, false)
	f.Add(3, 3, true)
	f.Add(1, 2, false)
	f.Add(5, 9, false)
	f.Add(4, 3, false)
	f.Add(-1, 4, true)
	f.Add(0, 0, false)
	f.Fuzz(func(t *testing.T, clientMin, clientMax int, probe bool) {
		got := NegotiateProtocol(clientMin, clientMax, probe)
		if got == 0 {
			// No overlap claimed: check that no version in our range satisfies the client.
			for v := MinProbeProtocolVersion; v <= ProtocolVersion; v++ {
				if v >= clientMin && v <= clientMax && (probe || v >= MinClientProtocolVersion) {
					t.Fatalf("NegotiateProtocol(%d, %d, %v) = 0 but %d is acceptable to both", clientMin, clientMax, probe, v)
				}
			}
			return
		}
		if got < clientMin || got > clientMax {
			t.Fatalf("NegotiateProtocol(%d, %d, %v) = %d outside client range", clientMin, clientMax, probe, got)
		}
		if got > ProtocolVersion || got < MinProbeProtocolVersion || (!probe && got < MinClientProtocolVersion) {
			t.Fatalf("NegotiateProtocol(%d, %d, %v) = %d outside server range", clientMin, clientMax, probe, got)
		}
	})
}

// authInputFromParams mirrors the server's reconstruction of the signed payload
// from connect params (token resolution order: token, deviceToken, bootstrapToken).
func authInputFromParams(p ConnectParams) DeviceAuthInput {
	in := DeviceAuthInput{
		ClientID: p.Client.ID, ClientMode: p.Client.Mode, Role: p.Role, Scopes: p.Scopes,
		Platform: p.Client.Platform, DeviceFamily: p.Client.DeviceFamily,
	}
	if p.Device != nil {
		in.DeviceID, in.SignedAtMs, in.Nonce = p.Device.ID, p.Device.SignedAt, p.Device.Nonce
	}
	if p.Auth != nil {
		switch {
		case p.Auth.Token != "":
			in.Token = p.Auth.Token
		case p.Auth.DeviceToken != "":
			in.Token = p.Auth.DeviceToken
		case p.Auth.BootstrapToken != "":
			in.Token = p.Auth.BootstrapToken
		}
	}
	return in
}

// equalJSON compares two raw JSON values structurally (whitespace-insensitive).
// Both empty counts as equal; an invalid side fails the test.
func equalJSON(t *testing.T, a, b json.RawMessage) bool {
	t.Helper()
	if len(a) == 0 || len(b) == 0 {
		return len(bytes.TrimSpace(a)) == 0 && len(bytes.TrimSpace(b)) == 0
	}
	var va, vb any
	if err := json.Unmarshal(a, &va); err != nil {
		t.Fatalf("invalid JSON %q: %v", a, err)
	}
	if err := json.Unmarshal(b, &vb); err != nil {
		t.Fatalf("invalid JSON %q: %v", b, err)
	}
	return reflect.DeepEqual(va, vb)
}
