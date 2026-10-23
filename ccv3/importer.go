// ClawEh
// License: MIT
//
// Copyright (c) 2026 Tenebris Technologies Inc.

package ccv3

import (
	"bytes"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"hash/crc32"
	"strings"

	"github.com/PivotLLM/ClawEh/internal/mergepatch"
)

// Format names what Import read: the card version and its container.
type Format string

// The formats Import accepts.
const (
	FormatV3JSON Format = "v3-json"
	FormatV2JSON Format = "v2-json"
	FormatV3PNG  Format = "v3-png"
	FormatV2PNG  Format = "v2-png"
)

// pngSignature starts every PNG file.
var pngSignature = []byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n'}

// The tEXt keywords that carry a card: V3 apps write ccv3 (and usually a V2
// copy in chara), V2 apps only chara.
const (
	keywordV3 = "ccv3"
	keywordV2 = "chara"
)

// msgV1 is the refusal for a V1 card (a flat object with no spec).
const msgV1 = "This is a V1 card; convert it to V2 or V3 first."

// parseImport reads raw (card JSON or a PNG card) into V3 data, with V2
// converted and unknown members kept. It does not set dates or default
// arrays; the store does that on write.
func parseImport(raw []byte) (json.RawMessage, Format, error) {
	if len(raw) > MaxImportBytes {
		return nil, "", refuse(ErrTooLarge, "The file is larger than %d MiB.", MaxImportBytes>>20)
	}
	png := bytes.HasPrefix(raw, pngSignature)
	if png {
		text, err := pngCard(raw)
		if err != nil {
			return nil, "", err
		}
		raw = text
	}
	if len(raw) > MaxCardBytes {
		return nil, "", refuse(ErrTooLarge, "The card is larger than %d MiB.", MaxCardBytes>>20)
	}
	data, v3, err := parseEnvelope(raw)
	if err != nil {
		return nil, "", err
	}
	if data, err = normalizeImport(data); err != nil {
		return nil, "", err
	}
	switch {
	case v3 && png:
		return data, FormatV3PNG, nil
	case v3:
		return data, FormatV3JSON, nil
	case png:
		return data, FormatV2PNG, nil
	default:
		return data, FormatV2JSON, nil
	}
}

// normalizeImport removes the known members of data, of character_book and
// of each lorebook entry whose value is null, as other apps write for a field
// they leave empty, so an imported card treats them as absent. A merge patch
// cannot reach into an array, so the entries are patched one by one and the
// array is replaced whole. It also turns a source given as one string, as
// some apps write it, into the list the spec defines, and drops a blank one.
func normalizeImport(data json.RawMessage) (json.RawMessage, error) {
	members, _ := object(data)
	patch := nullPatch(members, dataFields)
	if v, ok := members["source"]; ok && isString(v) {
		var s string
		if err := json.Unmarshal(v, &s); err != nil {
			return nil, fmt.Errorf("decode source: %w", err)
		}
		if isBlank(s) {
			patch["source"] = nil
		} else {
			patch["source"] = []json.RawMessage{v}
		}
	}
	if book, ok := object(members["character_book"]); ok {
		bookPatch := nullPatch(book, bookFields)
		entries, err := dropEntryNulls(book["entries"])
		if err != nil {
			return nil, err
		}
		if entries != nil {
			bookPatch["entries"] = entries
		}
		if len(bookPatch) > 0 {
			patch["character_book"] = bookPatch
		}
	}
	if len(patch) == 0 {
		return data, nil
	}
	return applyPatch(data, patch)
}

// nullPatch is the merge patch removing the members of fields that are null
// in members.
func nullPatch(members map[string]json.RawMessage, fields []field) map[string]any {
	patch := map[string]any{}
	for _, f := range fields {
		if v, ok := members[f.name]; ok && isNull(v) {
			patch[f.name] = nil
		}
	}
	return patch
}

// dropEntryNulls returns the lorebook entries with their null known members
// removed, or nil when raw is not an array or no entry has one.
func dropEntryNulls(raw json.RawMessage) ([]json.RawMessage, error) {
	entries, ok := array(raw)
	if !ok {
		return nil, nil
	}
	changed := false
	for i, e := range entries {
		m, ok := object(e)
		if !ok {
			continue
		}
		patch := nullPatch(m, entryFields)
		if len(patch) == 0 {
			continue
		}
		out, err := applyPatch(e, patch)
		if err != nil {
			return nil, err
		}
		entries[i], changed = out, true
	}
	if !changed {
		return nil, nil
	}
	return entries, nil
}

// applyPatch applies patch to target as a merge patch. The patch carries
// card text (entries, source), so it is encoded without HTML escaping, as
// the store writes it: "<START>" reads as written.
func applyPatch(target json.RawMessage, patch map[string]any) (json.RawMessage, error) {
	var encoded bytes.Buffer
	enc := json.NewEncoder(&encoded)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(patch); err != nil {
		return nil, fmt.Errorf("encode null removal: %w", err)
	}
	out, err := mergepatch.Apply(target, bytes.TrimSuffix(encoded.Bytes(), []byte("\n")))
	if err != nil {
		return nil, fmt.Errorf("remove null members: %w", err)
	}
	return out, nil
}

// parseEnvelope reads a V2 or V3 envelope and returns its data object and
// whether it was V3. A V2 card's data is V3 data with fewer members, so the
// conversion is the new envelope plus the members the store fills in.
func parseEnvelope(raw []byte) (json.RawMessage, bool, error) {
	env, ok := object(raw)
	if !ok {
		return nil, false, refuse(nil, "The file is not a character card.")
	}
	specRaw, ok := env["spec"]
	if !ok {
		return nil, false, refuse(nil, msgV1)
	}
	var spec string
	if err := json.Unmarshal(specRaw, &spec); err != nil {
		return nil, false, refuse(nil, "The card's spec must be text.")
	}
	if spec != SpecV3 && spec != specV2 {
		return nil, false, refuse(nil, "The card's spec %q is not supported; use a V2 or V3 card.", spec)
	}
	data, ok := env["data"]
	if !ok {
		return nil, false, refuse(nil, "The card has no data.")
	}
	if _, ok := object(data); !ok {
		return nil, false, refuse(nil, "The card's data must be an object.")
	}
	return data, spec == SpecV3, nil
}

// pngCard returns the card JSON in a PNG's tEXt chunks: the ccv3 chunk when
// there is one, else the chara chunk. It walks the chunks itself, since
// image/png does not expose text chunks.
func pngCard(raw []byte) ([]byte, error) {
	damaged := refuse(nil, "The PNG file is damaged.")
	found := map[string][]byte{}
	rest := raw[len(pngSignature):]
	for {
		const header, crcLen = 8, 4
		if len(rest) < header+crcLen {
			return nil, damaged
		}
		n := binary.BigEndian.Uint32(rest[:4])
		if int64(n) > int64(len(rest)-header-crcLen) {
			return nil, damaged
		}
		typ := string(rest[4:8])
		body := rest[header : header+int(n)]
		sum := binary.BigEndian.Uint32(rest[header+int(n):])
		rest = rest[header+int(n)+crcLen:]
		switch typ {
		case "IEND":
			return pickCard(found)
		case "tEXt":
			if crc32.ChecksumIEEE(crcInput(typ, body)) != sum {
				return nil, damaged
			}
			keyword, text, ok := bytes.Cut(body, []byte{0})
			k := strings.ToLower(string(keyword))
			if ok && (k == keywordV3 || k == keywordV2) {
				found[k] = text
			}
		}
	}
}

// crcInput is the bytes a chunk's CRC covers: its type and data.
func crcInput(typ string, body []byte) []byte {
	return append([]byte(typ), body...)
}

// pickCard decodes the preferred card chunk.
func pickCard(found map[string][]byte) ([]byte, error) {
	text, ok := found[keywordV3]
	if !ok {
		if text, ok = found[keywordV2]; !ok {
			return nil, refuse(nil, "The PNG file has no character card.")
		}
	}
	text = bytes.TrimSpace(text)
	if base64.StdEncoding.DecodedLen(len(text)) > MaxCardBytes+3 {
		return nil, refuse(ErrTooLarge, "The card is larger than %d MiB.", MaxCardBytes>>20)
	}
	out, err := base64.StdEncoding.DecodeString(string(text))
	if err != nil {
		if out, err = base64.RawStdEncoding.DecodeString(string(text)); err != nil {
			return nil, refuse(nil, "The PNG file's character card is not readable.")
		}
	}
	return out, nil
}
