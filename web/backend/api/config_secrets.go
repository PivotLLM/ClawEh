package api

import (
	"encoding/json"
	"net/url"
	"strings"

	"github.com/PivotLLM/ClawEh/config"
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
//
// Four shapes are covered:
//   - a credential string ("api_key", "…_token", "…_secret", "…password"),
//   - a list of credential strings ("api_keys"),
//   - a map whose VALUES are all treated as credentials ("env", "headers"):
//     env is where secrets go, whatever the variable is called, and a header
//     value such as Authorization is a credential by definition,
//   - a proxy URL ("proxy"), whose userinfo (user:password) is masked.

// The field-name predicates (config.IsSecretKey and friends) live in the config
// package, where the same set decides which fields accept an "env:"/"file:"
// secret reference. A reference string is not a secret, so masking leaves it
// readable: the operator sees where the value comes from, not the value.
// maskSecrets walks a decoded config and replaces every credential with a
// display form ("sk-****cdef"). It mutates v in place.
func maskSecrets(v any) {
	switch t := v.(type) {
	case map[string]any:
		for k, val := range t {
			switch {
			case config.IsSecretKey(k):
				if s, ok := val.(string); ok && s != "" {
					t[k] = maskSecretValue(s)
					continue
				}
			case config.IsSecretListKey(k):
				if list, ok := val.([]any); ok {
					maskStringList(list)
					continue
				}
			case config.IsSecretMapKey(k):
				if m, ok := val.(map[string]any); ok {
					maskMapValues(m)
					continue
				}
			case config.IsProxyKey(k):
				if s, ok := val.(string); ok && !config.IsSecretRef(s) {
					t[k] = maskProxyURL(s)
					continue
				}
			}
			maskSecrets(val)
		}
	case []any:
		for _, item := range t {
			maskSecrets(item)
		}
	}
}

// maskSecretValue masks a credential for display, leaving a secret reference
// ("env:NAME", "file:/path") as written: it names where the secret lives and
// is what the operator needs to see to edit it.
func maskSecretValue(s string) string {
	if config.IsSecretRef(s) {
		return s
	}
	return maskAPIKey(s)
}

// maskStringList masks every non-empty string element in place.
func maskStringList(list []any) {
	for i, item := range list {
		if s, ok := item.(string); ok && s != "" {
			list[i] = maskSecretValue(s)
		}
	}
}

// maskMapValues masks every non-empty string value in place, whatever its key.
func maskMapValues(m map[string]any) {
	for k, val := range m {
		if s, ok := val.(string); ok && s != "" {
			m[k] = maskSecretValue(s)
		}
	}
}

// maskProxyURL replaces the userinfo of a proxy URL with "****", so
// "http://user:pass@proxy:3128" is shown as "http://****@proxy:3128". A URL
// without userinfo is returned unchanged. A scheme-less "user:pass@host" is
// masked the same way by position.
func maskProxyURL(s string) string {
	if s == "" {
		return s
	}
	if u, err := url.Parse(s); err == nil && u.User != nil {
		u.User = nil
		return strings.Replace(u.String(), "://", "://****@", 1)
	}
	if !strings.Contains(s, "://") {
		if at := strings.LastIndex(s, "@"); at >= 0 {
			return "****" + s[at:]
		}
	}
	return s
}

// unmaskSecrets restores masked credentials in an incoming config from the
// stored one, so a client that reads the masked config and writes it back does
// not overwrite real keys with "****". A value is only restored when it still
// looks masked; a genuinely new credential is written through untouched.
//
// Objects are matched by key. Array elements are matched by identity — the
// first of "id", "name", "model_name" or "account" that both sides carry —
// falling back to position only when neither side has one.
//
// Position alone is not safe here. The WebUI reads the masked config, edits a
// list and writes the whole list back, so deleting the first Telegram bot would
// otherwise restore the deleted bot's token onto the survivor that took its
// index: a silent credential swap, not a visible failure.
func unmaskSecrets(incoming, stored any) {
	switch in := incoming.(type) {
	case map[string]any:
		st, ok := stored.(map[string]any)
		if !ok {
			return
		}
		for k, val := range in {
			prev, present := st[k]
			switch {
			case config.IsSecretKey(k) || config.IsProxyKey(k):
				if s, isStr := val.(string); isStr && isMasked(s) {
					if p, isStr := prev.(string); present && isStr {
						in[k] = p
					}
					continue
				}
			case config.IsSecretListKey(k):
				if list, isList := val.([]any); isList {
					if prevList, isList := prev.([]any); present && isList {
						unmaskStringList(list, prevList)
					}
					continue
				}
			case config.IsSecretMapKey(k):
				if m, isMap := val.(map[string]any); isMap {
					if prevMap, isMap := prev.(map[string]any); present && isMap {
						unmaskMapValues(m, prevMap)
					}
					continue
				}
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
			if prev, found := matchStoredElement(item, st, i); found {
				unmaskSecrets(item, prev)
			}
		}
	}
}

// unmaskStringList restores masked elements of a credential list. A masked
// element has no identity of its own, so it is matched to the stored element
// whose mask it is — the one at the same position first, then any other not
// yet used — and only when nothing masks to it is the same position taken.
func unmaskStringList(list, stored []any) {
	used := make([]bool, len(stored))
	matches := func(j int, s string) bool {
		p, ok := stored[j].(string)
		return ok && !used[j] && maskAPIKey(p) == s
	}
	restore := func(i int, s string) {
		if i < len(stored) && matches(i, s) {
			list[i], used[i] = stored[i], true
			return
		}
		for j := range stored {
			if matches(j, s) {
				list[i], used[j] = stored[j], true
				return
			}
		}
		if i < len(stored) && !used[i] {
			if p, ok := stored[i].(string); ok {
				list[i], used[i] = p, true
			}
		}
	}
	for i, item := range list {
		if s, ok := item.(string); ok && isMasked(s) {
			restore(i, s)
		}
	}
}

// unmaskMapValues restores masked values of an env/headers map by key.
func unmaskMapValues(m, stored map[string]any) {
	for k, val := range m {
		s, ok := val.(string)
		if !ok || !isMasked(s) {
			continue
		}
		if p, ok := stored[k].(string); ok {
			m[k] = p
		}
	}
}

// identityKeys are the fields that name an element of a config array, in the
// order they are tried. Every secret-carrying list in the config has one:
// providers use "name", telegram bots "id", models "model_name", secmsg
// accounts "account".
var identityKeys = []string{"id", "name", "model_name", "account"}

// matchStoredElement finds item's counterpart in stored. It prefers an identity
// match so that reordering or deleting list entries cannot move a credential
// onto the wrong element, and falls back to position only when neither side
// carries an identity field.
func matchStoredElement(item any, stored []any, idx int) (any, bool) {
	obj, ok := item.(map[string]any)
	if !ok {
		if idx < len(stored) {
			return stored[idx], true
		}
		return nil, false
	}

	for _, key := range identityKeys {
		want, isStr := obj[key].(string)
		if !isStr || want == "" {
			continue
		}
		for _, candidate := range stored {
			cobj, isObj := candidate.(map[string]any)
			if !isObj {
				continue
			}
			if got, isStr := cobj[key].(string); isStr && got == want {
				return candidate, true
			}
		}
		// The element names itself but no stored element matches: it is new, so
		// there is nothing to restore from. Returning here rather than falling
		// through to the positional match is the point — position would hand it
		// some other element's credential.
		return nil, false
	}

	if idx < len(stored) {
		return stored[idx], true
	}
	return nil, false
}

// isMasked reports whether s carries the mask marker written by maskAPIKey and
// maskProxyURL.
func isMasked(s string) bool { return strings.Contains(s, "****") }

// maskedConfigJSON renders cfg as the file holds it (secret references shown
// as references) with every literal credential masked.
func maskedConfigJSON(cfg *config.Config) ([]byte, error) {
	raw, err := config.MarshalWithSecretRefs(cfg)
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
// credentials replaced by the stored ones, given as the stored config's JSON.
func restoreMaskedSecrets(body, stored []byte) ([]byte, error) {
	var in any
	if err := json.Unmarshal(body, &in); err != nil {
		return nil, err
	}
	var st any
	if err := json.Unmarshal(stored, &st); err != nil {
		return nil, err
	}
	unmaskSecrets(in, st)
	return json.Marshal(in)
}
