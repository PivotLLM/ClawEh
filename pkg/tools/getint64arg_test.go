package tools

import (
	"testing"
)

func TestGetInt64Arg_Missing_ReturnsDefault(t *testing.T) {
	args := map[string]any{}
	got, err := getInt64Arg(args, "offset", 0)
	if err != nil {
		t.Fatalf("getInt64Arg() error = %v", err)
	}
	if got != 0 {
		t.Errorf("got = %d, want 0", got)
	}
}

func TestGetInt64Arg_MissingKey_CustomDefault(t *testing.T) {
	args := map[string]any{}
	got, err := getInt64Arg(args, "limit", 100)
	if err != nil {
		t.Fatalf("getInt64Arg() error = %v", err)
	}
	if got != 100 {
		t.Errorf("got = %d, want 100", got)
	}
}

func TestGetInt64Arg_Float64_Integer(t *testing.T) {
	args := map[string]any{"offset": float64(50)}
	got, err := getInt64Arg(args, "offset", 0)
	if err != nil {
		t.Fatalf("getInt64Arg() error = %v", err)
	}
	if got != 50 {
		t.Errorf("got = %d, want 50", got)
	}
}

func TestGetInt64Arg_Float64_NonInteger(t *testing.T) {
	args := map[string]any{"offset": float64(1.5)}
	_, err := getInt64Arg(args, "offset", 0)
	if err == nil {
		t.Fatal("expected error for non-integer float64")
	}
}

func TestGetInt64Arg_Int(t *testing.T) {
	args := map[string]any{"limit": int(200)}
	got, err := getInt64Arg(args, "limit", 0)
	if err != nil {
		t.Fatalf("getInt64Arg() error = %v", err)
	}
	if got != 200 {
		t.Errorf("got = %d, want 200", got)
	}
}

func TestGetInt64Arg_Int64(t *testing.T) {
	args := map[string]any{"size": int64(1024)}
	got, err := getInt64Arg(args, "size", 0)
	if err != nil {
		t.Fatalf("getInt64Arg() error = %v", err)
	}
	if got != 1024 {
		t.Errorf("got = %d, want 1024", got)
	}
}

func TestGetInt64Arg_String_Valid(t *testing.T) {
	args := map[string]any{"count": "42"}
	got, err := getInt64Arg(args, "count", 0)
	if err != nil {
		t.Fatalf("getInt64Arg() error = %v", err)
	}
	if got != 42 {
		t.Errorf("got = %d, want 42", got)
	}
}

func TestGetInt64Arg_String_Invalid(t *testing.T) {
	args := map[string]any{"count": "not-a-number"}
	_, err := getInt64Arg(args, "count", 0)
	if err == nil {
		t.Fatal("expected error for invalid string integer")
	}
}

func TestGetInt64Arg_UnsupportedType(t *testing.T) {
	args := map[string]any{"val": []int{1, 2, 3}}
	_, err := getInt64Arg(args, "val", 0)
	if err == nil {
		t.Fatal("expected error for unsupported type")
	}
}

func TestGetInt64Arg_Zero_Float64(t *testing.T) {
	args := map[string]any{"offset": float64(0)}
	got, err := getInt64Arg(args, "offset", 99)
	if err != nil {
		t.Fatalf("getInt64Arg() error = %v", err)
	}
	// Key exists with value 0, so default should not apply
	if got != 0 {
		t.Errorf("got = %d, want 0 (zero float64 should be returned, not default)", got)
	}
}
