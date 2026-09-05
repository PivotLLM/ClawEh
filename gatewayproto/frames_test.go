package gatewayproto

import (
	"encoding/json"
	"testing"
)

func TestFrameKind(t *testing.T) {
	cases := map[string]string{
		`{"type":"req","id":"1","method":"connect"}`: FrameReq,
		`{"type":"res","id":"1","ok":true}`:          FrameRes,
		`{"type":"event","event":"tick"}`:            FrameEvent,
	}
	for raw, want := range cases {
		got, err := FrameKind([]byte(raw))
		if err != nil {
			t.Fatalf("FrameKind(%s): %v", raw, err)
		}
		if got != want {
			t.Fatalf("FrameKind(%s)=%q want %q", raw, got, want)
		}
	}
	if _, err := FrameKind([]byte(`{"id":"1"}`)); err == nil {
		t.Fatal("expected error for missing type")
	}
}

func TestRequestFrame_ParamsRaw(t *testing.T) {
	raw := `{"type":"req","id":"c1","method":"connect","params":{"minProtocol":4,"maxProtocol":4}}`
	var f RequestFrame
	if err := json.Unmarshal([]byte(raw), &f); err != nil {
		t.Fatal(err)
	}
	if f.Method != "connect" {
		t.Fatalf("method=%q", f.Method)
	}
	var p ConnectParams
	if err := json.Unmarshal(f.Params, &p); err != nil {
		t.Fatal(err)
	}
	if p.MinProtocol != 4 || p.MaxProtocol != 4 {
		t.Fatalf("params=%+v", p)
	}
}

func TestResponseAndEventEncoding(t *testing.T) {
	// Success response carries payload, no error key.
	b, _ := json.Marshal(NewOKResponse("c1", HelloOk{Type: "hello-ok", Protocol: 4}))
	if got := string(b); got == "" || !json.Valid(b) {
		t.Fatalf("invalid response json: %s", got)
	}
	var rf ResponseFrame
	_ = json.Unmarshal(b, &rf)
	if rf.Type != FrameRes || !rf.OK || rf.Error != nil {
		t.Fatalf("unexpected response: %+v", rf)
	}

	// Error response: ok=false, error set, payload omitted.
	eb, _ := json.Marshal(NewErrorResponse("c1", NewError(CodeInvalidRequest, "bad", nil)))
	var ef ResponseFrame
	_ = json.Unmarshal(eb, &ef)
	if ef.OK || ef.Error == nil || ef.Error.Code != CodeInvalidRequest {
		t.Fatalf("unexpected error response: %s", eb)
	}

	// Event with seq.
	seq := uint64(7)
	evb, _ := json.Marshal(NewEvent("chat", map[string]any{"x": 1}, &seq))
	var evf EventFrame
	_ = json.Unmarshal(evb, &evf)
	if evf.Type != FrameEvent || evf.Event != "chat" || evf.Seq == nil || *evf.Seq != 7 {
		t.Fatalf("unexpected event: %s", evb)
	}
}
