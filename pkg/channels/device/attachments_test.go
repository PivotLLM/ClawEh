package device

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/PivotLLM/ClawEh/pkg/bus"
	"github.com/PivotLLM/ClawEh/pkg/config"
	"github.com/PivotLLM/ClawEh/pkg/media"
	"github.com/PivotLLM/ClawEh/pkg/utils"
)

// TestStoreInboundAttachments verifies a decoded photo is written to the media
// store and returned as a media:// ref the agent loop can resolve.
func TestStoreInboundAttachments(t *testing.T) {
	utils.SetMediaStagingDir(t.TempDir())
	defer utils.SetMediaStagingDir("")

	dc, err := NewDeviceChannel(config.DeviceChannelConfig{Enabled: true}, t.TempDir(), false, bus.NewMessageBus())
	if err != nil {
		t.Fatalf("NewDeviceChannel: %v", err)
	}
	defer func() { _ = dc.Stop(context.Background()) }()
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
	dc, err := NewDeviceChannel(config.DeviceChannelConfig{Enabled: true}, t.TempDir(), false, bus.NewMessageBus())
	if err != nil {
		t.Fatalf("NewDeviceChannel: %v", err)
	}
	defer func() { _ = dc.Stop(context.Background()) }()

	if refs := dc.storeInboundAttachments("device:dev1", "run1", []InboundAttachment{
		{MimeType: "image/jpeg", Data: []byte{1, 2, 3}},
	}); refs != nil {
		t.Fatalf("expected nil refs with no media store, got %v", refs)
	}
}
