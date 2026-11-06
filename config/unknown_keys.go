// ClawEh
// License: MIT

package config

import (
	"encoding/json"
	"maps"
	"reflect"
	"slices"
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
// retired boolean form), so the walk stops at them rather than second-guessing
// what they accept. Keys in silentlyRemovedKeys are not reported either.
func unknownConfigKeys(doc any) []string {
	var out []string
	collectUnknownKeys(doc, reflect.TypeFor[Config](), "", &out)
	out = slices.DeleteFunc(out, func(key string) bool { return silentlyRemovedKeys[key] })
	sort.Strings(out)
	return out
}

// UnknownPatchKeys returns, sorted, the dotted path of every key in a config
// patch (PATCH /api/config) that the config schema does not have: the keys
// unknownConfigKeys finds, the silently removed ones included. Free-form maps
// (env, headers, ...) accept any key, and a type with its own UnmarshalJSON
// is not looked into, as at load.
func UnknownPatchKeys(patch map[string]any) []string {
	var out []string
	collectUnknownKeys(patch, reflect.TypeFor[Config](), "", &out)
	sort.Strings(out)
	return out
}

// permissionsPrefix is the path prefix of the security.permissions block.
const permissionsPrefix = "security.permissions."

// unknownPermissionKeys returns the unknown keys of a decoded config.json
// that lie in its security.permissions block (the pre-0.6.4 top-level
// dir_mode and file_mode, say): the startup write of the block leaves them
// out.
func unknownPermissionKeys(doc any) []string {
	var out []string
	collectUnknownKeys(doc, reflect.TypeFor[Config](), "", &out)
	out = slices.DeleteFunc(out, func(key string) bool { return !strings.HasPrefix(key, permissionsPrefix) })
	sort.Strings(out)
	return out
}

// silentlyRemovedKeys are removed settings that an existing config.json may
// still hold and that are ignored without a warning: the discovery TTL and
// visible budget (and the older name of the TTL) stopped mattering when every
// tool began to be offered on every call, hidden ones deferred.
var silentlyRemovedKeys = map[string]bool{
	"tools.discovery.ttl":            true,
	"tools.discovery.ttl_max":        true,
	"tools.discovery.visible_budget": true,
}

var jsonUnmarshalerType = reflect.TypeFor[json.Unmarshaler]()

// collectUnknownKeys appends to out the path of every key in doc that type t
// has no field for.
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
