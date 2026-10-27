package device

import (
	"context"
	"encoding/binary"
	"os"
	"strings"
	"testing"

	"github.com/PivotLLM/ClawEh/bus"
	"github.com/PivotLLM/ClawEh/config"
	"github.com/PivotLLM/ClawEh/media"
	"github.com/PivotLLM/ClawEh/utils"
)

// TestStoreInboundAttachments verifies a decoded photo is written to the media
// store and returned as a media:// ref the agent loop can resolve.
func TestStoreInboundAttachments(t *testing.T) {
	utils.SetMediaStagingDir(t.TempDir(), 0, 0)
	defer utils.SetMediaStagingDir("", 0, 0)

	dc, err := NewDeviceChannel(config.DeviceChannelConfig{Enabled: true}, t.TempDir(), false, bus.NewMessageBus(), "", nil)
	if err != nil {
		t.Fatalf("NewDeviceChannel: %v", err)
	}
	defer func() {
		if stopErr := dc.Stop(context.Background()); stopErr != nil {
			t.Errorf("Stop: %v", stopErr)
		}
	}()
	store := media.NewFileMediaStore()
	dc.SetMediaStore(store)

	data := []byte{0xff, 0xd8, 0xff, 0xe0, 0x00, 0x10} // JPEG magic-ish
	refs := dc.storeInboundAttachments("device:dev1", "run1", []InboundAttachment{
		{MimeType: "image/jpeg", Name: "photo.jpg", Data: data},
	})
	if len(refs) != 1 || !strings.HasPrefix(refs[0], "media://") {
		t.Fatalf("expected one media:// ref, got %v", refs)
	}

	path, err := store.Resolve(refs[0])
	if err != nil {
		t.Fatalf("resolve ref: %v", err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read stored file: %v", err)
	}
	if len(got) != len(data) {
		t.Fatalf("stored file size = %d, want %d", len(got), len(data))
	}
	if !strings.HasSuffix(path, ".jpg") {
		t.Fatalf("expected .jpg extension, got %q", path)
	}
}

// TestStoreInboundAttachmentsNoStore is a no-op (no panic, nil refs) when no
// media store is injected.
func TestStoreInboundAttachmentsNoStore(t *testing.T) {
	dc, err := NewDeviceChannel(config.DeviceChannelConfig{Enabled: true}, t.TempDir(), false, bus.NewMessageBus(), "", nil)
	if err != nil {
		t.Fatalf("NewDeviceChannel: %v", err)
	}
	defer func() {
		if stopErr := dc.Stop(context.Background()); stopErr != nil {
			t.Errorf("Stop: %v", stopErr)
		}
	}()

	if refs := dc.storeInboundAttachments("device:dev1", "run1", []InboundAttachment{
		{MimeType: "image/jpeg", Data: []byte{1, 2, 3}},
	}); refs != nil {
		t.Fatalf("expected nil refs with no media store, got %v", refs)
	}
}

// TestStoreInboundAttachmentsStreamedWAV verifies a streamed WAV clip (header
// sizes left as placeholders) is stored with a .wav extension and a repaired
// header, so voice transcription accepts it.
func TestStoreInboundAttachmentsStreamedWAV(t *testing.T) {
	utils.SetMediaStagingDir(t.TempDir(), 0, 0)
	defer utils.SetMediaStagingDir("", 0, 0)

	dc, err := NewDeviceChannel(config.DeviceChannelConfig{Enabled: true}, t.TempDir(), false, bus.NewMessageBus(), "", nil)
	if err != nil {
		t.Fatalf("NewDeviceChannel: %v", err)
	}
	defer func() {
		if stopErr := dc.Stop(context.Background()); stopErr != nil {
			t.Errorf("Stop: %v", stopErr)
		}
	}()
	store := media.NewFileMediaStore()
	dc.SetMediaStore(store)

	payload := []byte{1, 2, 3, 4, 5, 6}
	refs := dc.storeInboundAttachments("device:dev1", "run1", []InboundAttachment{
		{MimeType: "audio/wav; rate=16000", Data: wavFixture(0xFFFFFFFF, 0xFFFFFFFF, nil, payload)},
	})
	if len(refs) != 1 {
		t.Fatalf("expected one ref, got %v", refs)
	}
	path, meta, err := store.ResolveWithMeta(refs[0])
	if err != nil {
		t.Fatalf("resolve ref: %v", err)
	}
	if !strings.HasSuffix(path, ".wav") || !strings.HasSuffix(meta.Filename, ".wav") {
		t.Fatalf("expected .wav path and filename, got %q / %q", path, meta.Filename)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read stored file: %v", err)
	}
	if r := binary.LittleEndian.Uint32(got[4:8]); r != uint32(len(got)-8) {
		t.Errorf("RIFF size = %d, want %d", r, len(got)-8)
	}
	if d := binary.LittleEndian.Uint32(got[40:44]); d != uint32(len(payload)) {
		t.Errorf("data size = %d, want %d", d, len(payload))
	}
}

// TestStoreInboundAttachmentsGenericTypeWAV verifies a WAV clip labelled with a
// generic MIME type is still stored as .wav (from its name) and still has its
// streamed header repaired, so voice transcription accepts it.
func TestStoreInboundAttachmentsGenericTypeWAV(t *testing.T) {
	utils.SetMediaStagingDir(t.TempDir(), 0, 0)
	defer utils.SetMediaStagingDir("", 0, 0)

	dc, err := NewDeviceChannel(config.DeviceChannelConfig{Enabled: true}, t.TempDir(), false, bus.NewMessageBus(), "", nil)
	if err != nil {
		t.Fatalf("NewDeviceChannel: %v", err)
	}
	defer func() {
		if stopErr := dc.Stop(context.Background()); stopErr != nil {
			t.Errorf("Stop: %v", stopErr)
		}
	}()
	store := media.NewFileMediaStore()
	dc.SetMediaStore(store)

	payload := []byte{1, 2, 3, 4, 5, 6}
	refs := dc.storeInboundAttachments("device:dev1", "run1", []InboundAttachment{
		{MimeType: "application/octet-stream", Name: "clip.wav", Data: wavFixture(0xFFFFFFFF, 0xFFFFFFFF, nil, payload)},
	})
	if len(refs) != 1 {
		t.Fatalf("expected one ref, got %v", refs)
	}
	path, err := store.Resolve(refs[0])
	if err != nil {
		t.Fatalf("resolve ref: %v", err)
	}
	if !strings.HasSuffix(path, ".wav") {
		t.Fatalf("expected .wav path, got %q", path)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read stored file: %v", err)
	}
	if d := binary.LittleEndian.Uint32(got[40:44]); d != uint32(len(payload)) {
		t.Errorf("data size = %d, want %d", d, len(payload))
	}
}
