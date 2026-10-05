// ClawEh
// License: MIT

package config

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
)

// Secret references.
//
// Any secret-bearing string in config.json may be written as a reference
// instead of a literal: "env:NAME" reads the environment variable NAME, and
// "file:/abs/path" reads the file (contents trimmed of surrounding whitespace;
// the file must be private, 0600 or tighter). References are resolved when
// the config is loaded, so the running Config carries the real value, and the
// reference string is put back when the config is saved, so the secret never
// lands in config.json.
//
// Which fields count as secret-bearing is decided by the JSON field name, with
// the same predicates the WebUI's masking uses (IsSecretKey and friends), so a
// new "…_token" field accepts a reference the day it is introduced.

// SecretRefEnvPrefix and SecretRefFilePrefix introduce the two reference forms.
const (
	SecretRefEnvPrefix  = "env:"
	SecretRefFilePrefix = "file:"
)

// IsSecretRef reports whether s is a secret reference rather than a literal.
func IsSecretRef(s string) bool {
	return strings.HasPrefix(s, SecretRefEnvPrefix) || strings.HasPrefix(s, SecretRefFilePrefix)
}

// IsSecretKey reports whether a JSON field name denotes a credential
// ("api_key", "…_token", "…_secret", "…password", "password_hash").
func IsSecretKey(key string) bool {
	k := strings.ToLower(key)
	switch {
	case k == "api_key" || strings.HasSuffix(k, "_api_key"):
		return true
	case k == "token" || strings.HasSuffix(k, "_token"):
		return true
	case k == "secret" || strings.HasSuffix(k, "_secret"):
		return true
	case k == "password" || strings.HasSuffix(k, "_password") || k == "password_hash":
		return true
	default:
		return false
	}
}

// IsSecretListKey reports whether a JSON field holds a list of credentials
// (the search providers' "api_keys" rotation lists).
func IsSecretListKey(key string) bool {
	k := strings.ToLower(key)
	return k == "api_keys" || strings.HasSuffix(k, "_api_keys")
}

// IsSecretMapKey reports whether every value of a JSON object field is a
// credential: process environment for MCP servers and CLI models, and HTTP
// headers for remote MCP servers.
func IsSecretMapKey(key string) bool {
	k := strings.ToLower(key)
	return k == "env" || k == "headers"
}

// IsProxyKey reports whether a JSON field is a proxy URL, which may embed
// credentials as userinfo.
func IsProxyKey(key string) bool {
	k := strings.ToLower(key)
	return k == "proxy" || strings.HasSuffix(k, "_proxy")
}

// secretRef records one resolved reference: where it was in the document, the
// reference string as written, and the value it resolved to. Save uses it to
// write the reference back in place of the value.
type secretRef struct {
	Path  string
	Ref   string
	Value string
}

// walkSecrets visits every string sitting in a secret position of a decoded
// JSON document — a credential field, an element of a credential list, a value
// of an env/headers map, or a proxy URL — and replaces it with what visit
// returns. path is the dotted location ("providers[0].api_key").
func walkSecrets(doc any, path string, visit func(path, value string) string) {
	switch t := doc.(type) {
	case map[string]any:
		for k, val := range t {
			p := joinPath(path, k)
			switch {
			case IsSecretKey(k) || IsProxyKey(k):
				if s, ok := val.(string); ok {
					t[k] = visit(p, s)
					continue
				}
			case IsSecretListKey(k):
				if list, ok := val.([]any); ok {
					for i, item := range list {
						if s, ok := item.(string); ok {
							list[i] = visit(p+"["+strconv.Itoa(i)+"]", s)
						}
					}
					continue
				}
			case IsSecretMapKey(k):
				if m, ok := val.(map[string]any); ok {
					for mk, mv := range m {
						if s, ok := mv.(string); ok {
							m[mk] = visit(joinPath(p, mk), s)
						}
					}
					continue
				}
			}
			walkSecrets(val, p, visit)
		}
	case []any:
		for i, item := range t {
			walkSecrets(item, path+"["+strconv.Itoa(i)+"]", visit)
		}
	}
}

func joinPath(prefix, key string) string {
	if prefix == "" {
		return key
	}
	return prefix + "." + key
}

// resolveSecretRefs replaces every secret reference in doc with its value and
// returns the references it resolved. A missing variable or an unreadable or
// loose file is an error naming the config key.
func resolveSecretRefs(doc any) ([]secretRef, error) {
	var refs []secretRef
	var firstErr error
	walkSecrets(doc, "", func(path, s string) string {
		if !IsSecretRef(s) || firstErr != nil {
			return s
		}
		v, err := resolveSecretRef(s)
		if err != nil {
			firstErr = fmt.Errorf("%s: %w", path, err)
			return s
		}
		refs = append(refs, secretRef{Path: path, Ref: s, Value: v})
		return v
	})
	if firstErr != nil {
		return nil, firstErr
	}
	return refs, nil
}

// resolveSecretRef dereferences one "env:NAME" or "file:/abs/path" string.
func resolveSecretRef(ref string) (string, error) {
	if name, ok := strings.CutPrefix(ref, SecretRefEnvPrefix); ok {
		name = strings.TrimSpace(name)
		if name == "" {
			return "", fmt.Errorf("secret reference %q names no environment variable", ref)
		}
		v, set := os.LookupEnv(name)
		if !set {
			return "", fmt.Errorf("secret reference %q: environment variable %s is not set", ref, name)
		}
		return v, nil
	}
	path := strings.TrimSpace(strings.TrimPrefix(ref, SecretRefFilePrefix))
	if path == "" || !filepath.IsAbs(path) {
		return "", fmt.Errorf("secret reference %q: the path must be absolute", ref)
	}
	fi, err := os.Stat(path)
	if err != nil {
		return "", fmt.Errorf("secret reference %q: %w", ref, err)
	}
	if fi.IsDir() {
		return "", fmt.Errorf("secret reference %q: %s is a directory", ref, path)
	}
	if runtime.GOOS != "windows" {
		if mode := fi.Mode().Perm(); mode&0o077 != 0 {
			return "", fmt.Errorf("secret reference %q: %s is readable by group or other (mode %04o); run: chmod 600 %s", ref, path, mode, path)
		}
	}
	data, err := os.ReadFile(path) //nolint:gosec // the operator named this file in config.json
	if err != nil {
		return "", fmt.Errorf("secret reference %q: %w", ref, err)
	}
	return strings.TrimSpace(string(data)), nil
}

// restoreSecretRefs puts references back into doc wherever the value being
// saved is one a reference resolved to. The reference recorded at the same
// path is preferred; failing that, any reference with that value is used, so a
// secret keeps its reference when list entries before it are deleted and its
// index shifts. A changed value is left as the literal: the operator replaced
// the reference.
func restoreSecretRefs(doc any, refs []secretRef) {
	if len(refs) == 0 {
		return
	}
	byPath := make(map[string]secretRef, len(refs))
	for _, r := range refs {
		byPath[r.Path] = r
	}
	walkSecrets(doc, "", func(path, s string) string {
		if s == "" || IsSecretRef(s) {
			return s
		}
		if r, ok := byPath[path]; ok && r.Value == s {
			return r.Ref
		}
		for _, r := range refs {
			if r.Value == s {
				return r.Ref
			}
		}
		return s
	})
}

// decodeDocument parses JSON into generic values, keeping numbers verbatim so
// a re-encode does not disturb large integers (Telegram ids, for one).
func decodeDocument(data []byte) (any, error) {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	var doc any
	if err := dec.Decode(&doc); err != nil {
		return nil, err
	}
	return doc, nil
}

// MarshalWithSecretRefs encodes cfg as it is written to disk: compact JSON with
// every secret reference the config was loaded with restored in place of the
// value it resolved to. It is what GET /api/config shows and what the file
// holds; json.Marshal(cfg) on its own yields the resolved values.
func MarshalWithSecretRefs(cfg *Config) ([]byte, error) {
	data, err := json.Marshal(cfg)
	if err != nil {
		return nil, err
	}
	if len(cfg.secretRefs) == 0 {
		return data, nil
	}
	doc, err := decodeDocument(data)
	if err != nil {
		return nil, err
	}
	restoreSecretRefs(doc, cfg.secretRefs)
	return json.Marshal(doc)
}

// resolveConfigSecrets resolves any secret references held as literals in cfg
// (a config the WebUI submitted with "env:NAME" in a field), returning a
// config whose fields carry the values and whose reference record is the union
// of cfg's existing record and the references resolved here. cfg itself is not
// modified. Runtime-only fields (the data dir) are carried over.
func resolveConfigSecrets(cfg *Config) (*Config, error) {
	data, err := json.Marshal(cfg)
	if err != nil {
		return nil, err
	}
	doc, err := decodeDocument(data)
	if err != nil {
		return nil, err
	}
	refs, err := resolveSecretRefs(doc)
	if err != nil {
		return nil, err
	}
	if len(refs) == 0 {
		return cfg, nil
	}
	resolved, err := json.Marshal(doc)
	if err != nil {
		return nil, err
	}
	out := &Config{}
	if err := json.Unmarshal(resolved, out); err != nil {
		return nil, err
	}
	out.dataDir = cfg.dataDir
	out.secretRefs = mergeSecretRefs(cfg.secretRefs, refs)
	return out, nil
}

// mergeSecretRefs returns old with newer laid over it: a newer reference at a
// path already recorded replaces the old one there.
func mergeSecretRefs(old, newer []secretRef) []secretRef {
	if len(newer) == 0 {
		return old
	}
	replaced := make(map[string]bool, len(newer))
	for _, r := range newer {
		replaced[r.Path] = true
	}
	out := make([]secretRef, 0, len(old)+len(newer))
	for _, r := range old {
		if !replaced[r.Path] {
			out = append(out, r)
		}
	}
	return append(out, newer...)
}

// SecretRefPaths returns the dotted config paths that were loaded from a secret
// reference, for diagnostics and tests. The values are not returned.
func (c *Config) SecretRefPaths() []string {
	out := make([]string, 0, len(c.secretRefs))
	for _, r := range c.secretRefs {
		out = append(out, r.Path)
	}
	return out
}
