// ClawEh
// License: MIT

package config

import (
	"encoding/json"
	"maps"
	"reflect"
	"sort"
	"strconv"
	"strings"

	"github.com/PivotLLM/ClawEh/logger"
)

// unknownConfigKeys walks a decoded config.json against the Config struct and
// returns the dotted path of every key the struct has no field for. The JSON
// decoder ignores such keys silently, which turns a typo into a setting that
// never takes effect; this is how the operator finds out.
//
// Types with their own UnmarshalJSON own their decoding (MaestroConfig accepts a
// retired boolean form, ToolDiscoveryConfig a renamed key), so the walk stops
// at them rather than second-guessing what they accept.
func unknownConfigKeys(doc any) []string {
	var out []string
	collectUnknownKeys(doc, reflect.TypeFor[Config](), "", &out)
	sort.Strings(out)
	return out
}

var jsonUnmarshalerType = reflect.TypeFor[json.Unmarshaler]()

func collectUnknownKeys(doc any, t reflect.Type, path string, out *[]string) {
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	if t != reflect.TypeFor[Config]() && reflect.PointerTo(t).Implements(jsonUnmarshalerType) {
		return
	}
	switch t.Kind() {
	case reflect.Struct:
		m, ok := doc.(map[string]any)
		if !ok {
			return
		}
		fields := jsonFields(t)
		for k, v := range m {
			ft, known := fields[k]
			if !known {
				*out = append(*out, joinPath(path, k))
				continue
			}
			collectUnknownKeys(v, ft, joinPath(path, k), out)
		}
	case reflect.Map:
		m, ok := doc.(map[string]any)
		if !ok {
			return
		}
		for k, v := range m {
			collectUnknownKeys(v, t.Elem(), joinPath(path, k), out)
		}
	case reflect.Slice, reflect.Array:
		list, ok := doc.([]any)
		if !ok {
			return
		}
		for i, v := range list {
			collectUnknownKeys(v, t.Elem(), path+"["+strconv.Itoa(i)+"]", out)
		}
	default:
		// Scalars have no keys to descend into.
	}
}

// jsonFields maps the JSON names a struct decodes to their field types,
// flattening embedded structs the way encoding/json does.
func jsonFields(t reflect.Type) map[string]reflect.Type {
	out := map[string]reflect.Type{}
	for f := range t.Fields() {
		tag := f.Tag.Get("json")
		if tag == "-" {
			continue
		}
		name, _, _ := strings.Cut(tag, ",")
		if f.Anonymous && name == "" {
			et := f.Type
			for et.Kind() == reflect.Pointer {
				et = et.Elem()
			}
			if et.Kind() == reflect.Struct {
				maps.Copy(out, jsonFields(et))
				continue
			}
		}
		if f.PkgPath != "" {
			continue // unexported
		}
		if name == "" {
			name = f.Name
		}
		out[name] = f.Type
	}
	return out
}

// warnUnknownKeys logs each unknown key once at WARN. Loading never fails on
// an unknown key: the config may have been written by a newer build, and a
// warning is the right weight for a typo.
func warnUnknownKeys(doc any) {
	for _, key := range unknownConfigKeys(doc) {
		logger.WarnCF("config", "unknown config key: "+key, map[string]any{"key": key})
	}
}
