package providers

import (
	"encoding/json"
	"testing"
)

// Tests for parseToolCallArguments — not covered by existing tests.

func TestParseToolCallArguments_Empty(t *testing.T) {
	args, str := parseToolCallArguments(nil)
	if args != nil {
		t.Errorf("expected nil args for empty input, got %v", args)
	}
	if str != "" {
		t.Errorf("expected empty string, got %q", str)
	}
}

func TestParseToolCallArguments_EmptyRawMessage(t *testing.T) {
	args, str := parseToolCallArguments(json.RawMessage{})
	if args != nil {
		t.Errorf("expected nil args for empty RawMessage, got %v", args)
	}
	if str != "" {
		t.Errorf("expected empty string, got %q", str)
	}
}

func TestParseToolCallArguments_JSONObject(t *testing.T) {
	raw := json.RawMessage(`{"path":"/tmp","content":"hello"}`)
	args, str := parseToolCallArguments(raw)
	if args == nil {
		t.Fatal("expected non-nil args for JSON object")
	}
	if args["path"] != "/tmp" {
		t.Errorf("args[path] = %v, want /tmp", args["path"])
	}
	if str == "" {
		t.Error("expected non-empty string for JSON object")
	}
}

func TestParseToolCallArguments_JSONEncodedString(t *testing.T) {
	// Arguments is a JSON-encoded string that itself is a JSON object.
	inner := `{"location":"NYC","units":"celsius"}`
	encoded, _ := json.Marshal(inner)
	args, str := parseToolCallArguments(json.RawMessage(encoded))
	if args == nil {
		t.Fatal("expected non-nil args from JSON-encoded string")
	}
	if args["location"] != "NYC" {
		t.Errorf("args[location] = %v, want NYC", args["location"])
	}
	if str == "" {
		t.Error("expected non-empty canonical string")
	}
}

func TestParseToolCallArguments_JSONEncodedString_InvalidInnerJSON(t *testing.T) {
	// Outer is a valid JSON string, but inner content is not valid JSON object.
	inner := "not json"
	encoded, _ := json.Marshal(inner)
	args, str := parseToolCallArguments(json.RawMessage(encoded))
	// Should return nil map but the decoded inner string.
	if args != nil {
		t.Errorf("expected nil args for invalid inner JSON, got %v", args)
	}
	if str != inner {
		t.Errorf("str = %q, want %q", str, inner)
	}
}

func TestParseToolCallArguments_InvalidJSONObject(t *testing.T) {
	raw := json.RawMessage(`{invalid}`)
	args, str := parseToolCallArguments(raw)
	if args != nil {
		t.Errorf("expected nil args for invalid JSON, got %v", args)
	}
	if str == "" {
		t.Error("expected non-empty string fallback for invalid JSON")
	}
}

func TestParseToolCallArguments_NonObjectNonString(t *testing.T) {
	// Something that starts with neither '{' nor '"'.
	raw := json.RawMessage(`42`)
	args, str := parseToolCallArguments(raw)
	if args != nil {
		t.Errorf("expected nil args for number, got %v", args)
	}
	if str == "" {
		t.Error("expected non-empty fallback string")
	}
}

func TestParseToolCallArguments_JSONEncodedString_InvalidOuterJSON(t *testing.T) {
	// Starts with '"' but isn't a valid JSON string.
	raw := json.RawMessage(`"invalid-json-string`)
	args, str := parseToolCallArguments(raw)
	if args != nil {
		t.Errorf("expected nil args, got %v", args)
	}
	// Falls back to the trimmed raw string.
	if str == "" {
		t.Error("expected non-empty fallback string")
	}
}
