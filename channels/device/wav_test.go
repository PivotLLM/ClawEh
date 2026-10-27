package device

import (
	"bytes"
	"encoding/binary"
	"testing"
)

// wavFixture builds a WAV file: RIFF header, a 16-byte "fmt " chunk, any extra
// chunks, then a "data" chunk with the given payload. riffSize and dataSize are
// written verbatim so tests can supply placeholders.
func wavFixture(riffSize, dataSize uint32, extra []byte, payload []byte) []byte {
	var b bytes.Buffer
	le := func(v uint32) {
		var tmp [4]byte
		binary.LittleEndian.PutUint32(tmp[:], v)
		b.Write(tmp[:])
	}
	b.WriteString("RIFF")
	le(riffSize)
	b.WriteString("WAVE")
	b.WriteString("fmt ")
	le(16)
	b.Write([]byte{1, 0, 1, 0, 0x80, 0x3e, 0, 0, 0, 0x7d, 0, 0, 2, 0, 16, 0}) // PCM, mono, 16 kHz, 16-bit
	b.Write(extra)
	b.WriteString("data")
	le(dataSize)
	b.Write(payload)
	return b.Bytes()
}

// chunk builds one RIFF sub-chunk, padded to an even length.
func chunk(id string, payload []byte) []byte {
	out := []byte(id)
	out = binary.LittleEndian.AppendUint32(out, uint32(len(payload)))
	out = append(out, payload...)
	if len(payload)%2 == 1 {
		out = append(out, 0)
	}
	return out
}

func TestRepairWAVSizes(t *testing.T) {
	payload := []byte{1, 2, 3, 4, 5, 6, 7, 8}
	list := chunk("LIST", []byte("INFOabc")) // odd length, exercises padding

	// Correct sizes for the plain fixture: 44-byte header + payload.
	plainLen := uint32(44 + len(payload))
	extraLen := plainLen + uint32(len(list))

	tests := []struct {
		name     string
		in       []byte
		wantRIFF uint32
		wantData uint32
		dataOff  int // offset of the data size field; 0 = expect input unchanged
	}{
		{
			name:     "valid header untouched",
			in:       wavFixture(plainLen-8, uint32(len(payload)), nil, payload),
			wantRIFF: plainLen - 8, wantData: uint32(len(payload)), dataOff: 40,
		},
		{
			name:     "placeholder sizes repaired",
			in:       wavFixture(0xFFFFFFFF, 0xFFFFFFFF, nil, payload),
			wantRIFF: plainLen - 8, wantData: uint32(len(payload)), dataOff: 40,
		},
		{
			name:     "zero sizes repaired",
			in:       wavFixture(0, 0, nil, payload),
			wantRIFF: plainLen - 8, wantData: uint32(len(payload)), dataOff: 40,
		},
		{
			name:     "data size larger than payload repaired",
			in:       wavFixture(plainLen-8, 1000, nil, payload),
			wantRIFF: plainLen - 8, wantData: uint32(len(payload)), dataOff: 40,
		},
		{
			name:     "extra chunk before data handled",
			in:       wavFixture(0xFFFFFFFF, 0xFFFFFFFF, list, payload),
			wantRIFF: extraLen - 8, wantData: uint32(len(payload)), dataOff: 40 + len(list),
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			orig := append([]byte(nil), tt.in...)
			got := repairWAVSizes(tt.in)
			if !bytes.Equal(tt.in, orig) {
				t.Fatal("input slice was modified")
			}
			if len(got) != len(tt.in) {
				t.Fatalf("len = %d, want %d", len(got), len(tt.in))
			}
			if r := binary.LittleEndian.Uint32(got[4:8]); r != tt.wantRIFF {
				t.Errorf("RIFF size = %d, want %d", r, tt.wantRIFF)
			}
			if d := binary.LittleEndian.Uint32(got[tt.dataOff : tt.dataOff+4]); d != tt.wantData {
				t.Errorf("data size = %d, want %d", d, tt.wantData)
			}
			if !bytes.Equal(got[tt.dataOff+4:], payload) {
				t.Error("payload changed")
			}
		})
	}
}

func TestRepairWAVSizesUnchanged(t *testing.T) {
	full := wavFixture(0xFFFFFFFF, 0xFFFFFFFF, nil, []byte{1, 2, 3, 4})
	tests := []struct {
		name string
		in   []byte
	}{
		{name: "nil", in: nil},
		{name: "not RIFF", in: []byte("\xff\xd8\xff\xe0 this is a jpeg, honest")},
		{name: "RIFF but not WAVE", in: append([]byte("RIFF\x10\x00\x00\x00AVI "), make([]byte, 16)...)},
		{name: "header only", in: full[:12]},
		{name: "truncated inside fmt chunk", in: full[:30]},
		{name: "truncated inside data header", in: full[:40]},
		{name: "no data chunk", in: append(append([]byte("RIFF\x00\x00\x00\x00WAVE"), chunk("fmt ", make([]byte, 16))...), chunk("LIST", []byte("x"))...)},
		{name: "chunk size placeholder before data", in: append([]byte("RIFF\xff\xff\xff\xffWAVEfmt \xff\xff\xff\xff"), make([]byte, 32)...)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := repairWAVSizes(tt.in)
			if !bytes.Equal(got, tt.in) {
				t.Fatalf("got % x, want input unchanged % x", got, tt.in)
			}
		})
	}
}

func TestExtForMIME(t *testing.T) {
	tests := []struct {
		mime string
		want string
	}{
		{"image/jpeg", ".jpg"},
		{"image/png", ".png"},
		{"audio/wav", ".wav"},
		{"audio/x-wav", ".wav"},
		{"audio/wave", ".wav"},
		{"audio/vnd.wave", ".wav"},
		{"audio/wav; rate=16000", ".wav"},
		{"Audio/WAV ;codecs=1", ".wav"},
		{"audio/ogg", ".ogg"},
		{"application/ogg", ".ogg"},
		{"audio/opus", ".ogg"},
		{"audio/ogg; codecs=opus", ".ogg"},
		{"audio/mpeg", ".mp3"},
		{"audio/mp3", ".mp3"},
		{"audio/mp4", ".m4a"},
		{"audio/x-m4a", ".m4a"},
		{"audio/m4a", ".m4a"},
		{"audio/flac", ".flac"},
		{"audio/x-flac", ".flac"},
		{"audio/aac", ".aac"},
		{"audio/x-aac", ".aac"},
		{"audio/webm", ".webm"},
		{"audio/webm;codecs=opus", ".webm"},
		{"audio/unknown", ".bin"},
		{"", ".bin"},
	}
	for _, tt := range tests {
		t.Run(tt.mime, func(t *testing.T) {
			if got := extForMIME(tt.mime); got != tt.want {
				t.Errorf("extForMIME(%q) = %q, want %q", tt.mime, got, tt.want)
			}
		})
	}
}

func TestAttachmentExt(t *testing.T) {
	tests := []struct {
		mime, name, want string
	}{
		{"audio/wav", "clip.bin", ".wav"},                // known type wins over the name
		{"application/octet-stream", "clip.wav", ".wav"}, // generic type: name decides
		{"", "Clip.WAV", ".wav"},                         // extension is lower-cased
		{"application/octet-stream", "clip", ".bin"},     // no extension anywhere
		{"application/octet-stream", "clip.", ".bin"},    // bare dot is not an extension
		{"", "", ".bin"},
	}
	for _, tt := range tests {
		t.Run(tt.mime+"/"+tt.name, func(t *testing.T) {
			if got := attachmentExt(tt.mime, tt.name); got != tt.want {
				t.Errorf("attachmentExt(%q, %q) = %q, want %q", tt.mime, tt.name, got, tt.want)
			}
		})
	}
}
