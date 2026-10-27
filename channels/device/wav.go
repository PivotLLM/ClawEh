package device

import (
	"bytes"
	"encoding/binary"
	"math"
)

// wavSizePlaceholder is the RIFF/data chunk size a streaming writer puts in the
// header when it does not yet know the final length.
const wavSizePlaceholder = 0xFFFFFFFF

// repairWAVSizes fixes the RIFF and "data" chunk size fields of a WAV file whose
// header was written before the length was known (a device streaming a clip as
// it records writes 0xFFFFFFFF or 0 there). The "data" size is rewritten only
// when it is a placeholder, zero, or does not fit the bytes actually received;
// the RIFF size whenever it differs from the file length minus 8, which is the
// only value it can legitimately hold. Input that is not a RIFF/WAVE file, has
// no "data" chunk, is truncated, or is too large for 32-bit size fields is
// returned unchanged. The input slice is never modified; a repaired copy is
// returned.
func repairWAVSizes(b []byte) []byte {
	const headerLen = 12 // "RIFF" + size + "WAVE"
	if uint64(len(b)) > math.MaxUint32 || len(b) < headerLen || !bytes.Equal(b[0:4], []byte("RIFF")) || !bytes.Equal(b[8:12], []byte("WAVE")) {
		return b
	}

	// Walk the chunks after the header to find "data".
	dataSizeOff := -1
	for off := headerLen; off+8 <= len(b); {
		id := b[off : off+4]
		size := binary.LittleEndian.Uint32(b[off+4 : off+8])
		if bytes.Equal(id, []byte("data")) {
			dataSizeOff = off + 4
			break
		}
		// Skip this chunk (payload is padded to an even length). Compare in
		// int64 so a huge or placeholder size cannot overflow.
		skip := int64(size) + int64(size&1)
		if skip > int64(len(b)-off-8) {
			return b // truncated or malformed chunk before "data"
		}
		off += 8 + int(skip)
	}
	if dataSizeOff < 0 {
		return b
	}

	payloadOff := dataSizeOff + 4
	wantData := uint32(len(b) - payloadOff) //nolint:gosec // G115: len(b) <= MaxUint32 is checked above
	wantRIFF := uint32(len(b) - 8)          //nolint:gosec // G115: len(b) <= MaxUint32 is checked above

	dataSize := binary.LittleEndian.Uint32(b[dataSizeOff:payloadOff])
	riffSize := binary.LittleEndian.Uint32(b[4:8])
	fixData := dataSize == wavSizePlaceholder || dataSize == 0 || uint64(dataSize) > uint64(wantData)
	fixRIFF := riffSize != wantRIFF
	if !fixData && !fixRIFF {
		return b
	}

	out := make([]byte, len(b))
	copy(out, b)
	if fixData {
		binary.LittleEndian.PutUint32(out[dataSizeOff:payloadOff], wantData)
	}
	if fixRIFF {
		binary.LittleEndian.PutUint32(out[4:8], wantRIFF)
	}
	return out
}
