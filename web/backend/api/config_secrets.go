package api

import (
	"encoding/json"
	"strings"
)

// Secret handling for GET/PUT/PATCH /api/config.
//
// The config endpoint returns the whole configuration, which carries every
// credential the gateway holds: provider API keys, bot tokens, device tokens,
// the WebUI channel token, search-provider keys. Those are masked on the way
// out and restored on the way back in, so an operator can read and edit the
// config without the response being a credential dump.
//
// Masking is driven by the JSON field name rather than a hand-maintained list
// of struct fields. A list has to be updated every time a credential is added
// to the config, and the failure mode when someone forgets is a silent leak.
// Matching on the name means a new "…_token" or "…_secret" field is covered the
// day it is introduced.

// isSecretKey reports whether a JSON field name denotes a credential.
//
// The value must also be a non-empty string for masking to apply, which is what
// keeps numeric fields like chars_per_token — a suffix match, but an int — from
// being treated as secrets.
func isSecretKey(key string) bool {
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

// maskSecrets walks a decoded config and replaces every credential with a
// display form ("sk-****cdef"). It mutates v in place.
func maskSecrets(v any) {
	switch t := v.(type) {
	case map[string]any:
		for k, val := range t {
			if s, ok := val.(string); ok && s != "" && isSecretKey(k) {
				t[k] = maskAPIKey(s)
				continue
			}
			maskSecrets(val)
		}
	case []any:
		for _, item := range t {
			maskSecrets(item)
		}
	}
}

// unmaskSecrets restores masked credentials in an incoming config from the
// stored one, so a client that reads the masked config and writes it back does
// not overwrite real keys with "****". A value is only restored when it still
// looks masked; a genuinely new credential is written through untouched.
//
// Objects are matched by key and arrays by index, which mirrors what the
// per-provider endpoint already does (web/backend/api/providers.go). Reordering
// an array in the same request that keeps a masked value is therefore not
// supported — set the credential explicitly in that case.
func unmaskSecrets(incoming, stored any) {
	switch in := incoming.(type) {
	case map[string]any:
		st, ok := stored.(map[string]any)
		if !ok {
			return
		}
		for k, val := range in {
			prev, present := st[k]
			if s, isStr := val.(string); isStr && isSecretKey(k) && isMasked(s) {
				if p, isStr := prev.(string); present && isStr {
					in[k] = p
				}
				continue
			}
			if present {
				unmaskSecrets(val, prev)
			}
		}
	case []any:
		st, ok := stored.([]any)
		if !ok {
			return
		}
		for i, item := range in {
			if i < len(st) {
				unmaskSecrets(item, st[i])
			}
		}
	}
}

// isMasked reports whether s carries the mask marker written by maskAPIKey.
func isMasked(s string) bool { return strings.Contains(s, "****") }

// maskedConfigJSON marshals cfg with every credential masked.
func maskedConfigJSON(cfg any) ([]byte, error) {
	raw, err := json.Marshal(cfg)
	if err != nil {
		return nil, err
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, err
	}
	maskSecrets(m)
	return json.Marshal(m)
}

// restoreMaskedSecrets takes a request body and returns it with masked
// credentials replaced by the ones currently on disk.
func restoreMaskedSecrets(body []byte, stored any) ([]byte, error) {
	var in any
	if err := json.Unmarshal(body, &in); err != nil {
		return nil, err
	}
	rawStored, err := json.Marshal(stored)
	if err != nil {
		return nil, err
	}
	var st any
	if err := json.Unmarshal(rawStored, &st); err != nil {
		return nil, err
	}
	unmaskSecrets(in, st)
	return json.Marshal(in)
}
