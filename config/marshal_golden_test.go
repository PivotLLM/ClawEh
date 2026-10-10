package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/PivotLLM/ClawEh/global"
)

// TestConfigMarshalGolden pins the JSON a saved config is written as: key
// names, field order and the omit rules. One config has every exported field
// set (reflectively, so a new field shows up here too) and one is the
// built-in default. Regenerate with UPDATE_GOLDEN=1 only when the shape is
// meant to change.
func TestConfigMarshalGolden(t *testing.T) {
	t.Setenv(global.EnvVarHome, "/claw-home")
	full := &Config{}
	fillValue(reflect.ValueOf(full).Elem(), map[reflect.Type]int{})

	cases := []struct {
		name   string
		cfg    *Config
		golden string
	}{
		{"full", full, "marshal_full.golden.json"},
		{"default", DefaultConfig(), "marshal_default.golden.json"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := json.MarshalIndent(tc.cfg, "", "  ")
			if err != nil {
				t.Fatalf("marshal: %v", err)
			}
			path := filepath.Join("testdata", tc.golden)
			if os.Getenv("UPDATE_GOLDEN") == "1" {
				if werr := os.WriteFile(path, got, 0o644); werr != nil {
					t.Fatalf("write golden: %v", werr)
				}
				return
			}
			want, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("read golden: %v", err)
			}
			if string(got) != string(want) {
				t.Fatalf("marshalled config differs from %s; rerun with UPDATE_GOLDEN=1 if intended", path)
			}
		})
	}
}

var rawMessageType = reflect.TypeFor[json.RawMessage]()

// fillValue sets every exported, settable field below v to a non-zero value.
// A type already being filled on the current path is left zero, which stops
// recursive types.
func fillValue(v reflect.Value, seen map[reflect.Type]int) {
	t := v.Type()
	if seen[t] > 0 {
		return
	}
	seen[t]++
	defer func() { seen[t]-- }()

	if t == rawMessageType {
		v.SetBytes([]byte(`"x"`))
		return
	}
	switch v.Kind() {
	case reflect.String:
		v.SetString("x")
	case reflect.Bool:
		v.SetBool(true)
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		v.SetInt(1)
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		v.SetUint(1)
	case reflect.Float32, reflect.Float64:
		v.SetFloat(1.5)
	case reflect.Pointer:
		p := reflect.New(t.Elem())
		fillValue(p.Elem(), seen)
		v.Set(p)
	case reflect.Slice:
		s := reflect.MakeSlice(t, 1, 1)
		fillValue(s.Index(0), seen)
		v.Set(s)
	case reflect.Map:
		m := reflect.MakeMap(t)
		k := reflect.New(t.Key()).Elem()
		fillValue(k, seen)
		e := reflect.New(t.Elem()).Elem()
		fillValue(e, seen)
		m.SetMapIndex(k, e)
		v.Set(m)
	case reflect.Interface:
		if t.NumMethod() == 0 {
			v.Set(reflect.ValueOf("x"))
		}
	case reflect.Struct:
		for i := 0; i < t.NumField(); i++ {
			if t.Field(i).IsExported() {
				fillValue(v.Field(i), seen)
			}
		}
	default:
		// Arrays, channels, funcs and complex numbers do not occur in the
		// config; they stay zero.
	}
}
