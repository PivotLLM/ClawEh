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
	if data, err = dropNulls(data); err != nil {
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

// dropNulls removes the known members of data and of character_book whose
// value is null, as other apps write for a field they leave empty, so an
// imported card treats them as absent. Lorebook entries are left as they
// are (a merge patch cannot reach into an array).
func dropNulls(data json.RawMessage) (json.RawMessage, error) {
	members, _ := object(data)
	patch := map[string]any{}
	for _, f := range dataFields {
		if v, ok := members[f.name]; ok && isNull(v) {
			patch[f.name] = nil
		}
	}
	if book, ok := object(members["character_book"]); ok {
		bookPatch := map[string]any{}
		for _, f := range bookFields {
			if v, ok := book[f.name]; ok && isNull(v) {
				bookPatch[f.name] = nil
			}
		}
		if len(bookPatch) > 0 {
			patch["character_book"] = bookPatch
		}
	}
	if len(patch) == 0 {
		return data, nil
	}
	encoded, err := json.Marshal(patch)
	if err != nil {
		return nil, fmt.Errorf("encode null removal: %w", err)
	}
	out, err := mergepatch.Apply(data, encoded)
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
