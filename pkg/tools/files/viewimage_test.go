package files

import (
	"bytes"
	"context"
	"encoding/base64"
	"image"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/PivotLLM/ClawEh/pkg/global"
)

func makePNG(t *testing.T, w, h int) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatalf("encode png: %v", err)
	}
	return buf.Bytes()
}

// decodeDataURL extracts and decodes the image carried by a data: URL.
func decodeDataURL(t *testing.T, url string) image.Image {
	t.Helper()
	i := strings.Index(url, ";base64,")
	if !strings.HasPrefix(url, "data:image/") || i < 0 {
		t.Fatalf("not an image data URL: %.40s", url)
	}
	raw, err := base64.StdEncoding.DecodeString(url[i+len(";base64,"):])
	if err != nil {
		t.Fatalf("base64 decode: %v", err)
	}
	img, _, err := image.Decode(bytes.NewReader(raw))
	if err != nil {
		t.Fatalf("decode image: %v", err)
	}
	return img
}

func TestViewImage_DownscalesOversized(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "big.png"), makePNG(t, 4000, 3000), 0o644); err != nil {
		t.Fatal(err)
	}
	res := NewViewImageTool(dir, true).Execute(context.Background(), map[string]any{"path": "big.png"})
	if res.IsError {
		t.Fatalf("unexpected error: %s", res.ForLLM)
	}
	if len(res.Images) != 1 {
		t.Fatalf("want 1 image, got %d", len(res.Images))
	}
	if !strings.Contains(res.ForLLM, "downscaled") {
		t.Errorf("expected a downscale note, got %q", res.ForLLM)
	}
	img := decodeDataURL(t, res.Images[0])
	b := img.Bounds()
	longest := b.Dx()
	if b.Dy() > longest {
		longest = b.Dy()
	}
	if longest != global.ImageDownscaleMaxEdgePx {
		t.Errorf("longest edge = %d, want %d", longest, global.ImageDownscaleMaxEdgePx)
	}
	// Aspect ratio preserved: 4000×3000 → 2048×1536.
	if b.Dx() != 2048 || b.Dy() != 1536 {
		t.Errorf("dims = %d×%d, want 2048×1536", b.Dx(), b.Dy())
	}
}

func TestViewImage_SmallPassesThrough(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "small.png"), makePNG(t, 800, 600), 0o644); err != nil {
		t.Fatal(err)
	}
	res := NewViewImageTool(dir, true).Execute(context.Background(), map[string]any{"path": "small.png"})
	if res.IsError || len(res.Images) != 1 {
		t.Fatalf("unexpected result: %+v", res)
	}
	if strings.Contains(res.ForLLM, "downscaled") {
		t.Errorf("small image should not be downscaled: %q", res.ForLLM)
	}
	if b := decodeDataURL(t, res.Images[0]).Bounds(); b.Dx() != 800 || b.Dy() != 600 {
		t.Errorf("dims = %d×%d, want 800×600 (untouched)", b.Dx(), b.Dy())
	}
}

func TestViewImage_RejectsNonImage(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "notes.txt"), []byte("just text, not an image"), 0o644); err != nil {
		t.Fatal(err)
	}
	res := NewViewImageTool(dir, true).Execute(context.Background(), map[string]any{"path": "notes.txt"})
	if !res.IsError || !strings.Contains(res.ForLLM, "not an image") {
		t.Fatalf("expected non-image rejection, got %+v", res)
	}
}

func TestViewImage_RejectsOversized(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "img.png"), makePNG(t, 100, 100), 0o644); err != nil {
		t.Fatal(err)
	}
	tool := NewViewImageTool(dir, true)
	tool.maxSize = 10 // force the cap below the file size
	res := tool.Execute(context.Background(), map[string]any{"path": "img.png"})
	if !res.IsError || !strings.Contains(res.ForLLM, "limit") {
		t.Fatalf("expected size-limit rejection, got %+v", res)
	}
}

// The optional focus arg is accepted and surfaced as a parseable ForLLM line so
// the vision-describe side-model (Flow A) can prioritize it.
func TestViewImage_FocusReflectedInForLLM(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "img.png"), makePNG(t, 50, 50), 0o644); err != nil {
		t.Fatal(err)
	}
	tool := NewViewImageTool(dir, true)

	res := tool.Execute(context.Background(), map[string]any{"path": "img.png", "focus": "count the cats"})
	if res.IsError {
		t.Fatalf("unexpected error: %+v", res)
	}
	if !strings.Contains(res.ForLLM, "Focus: count the cats") {
		t.Fatalf("focus not reflected in ForLLM: %q", res.ForLLM)
	}

	// No focus => no Focus line.
	res2 := tool.Execute(context.Background(), map[string]any{"path": "img.png"})
	if strings.Contains(res2.ForLLM, "Focus:") {
		t.Fatalf("unexpected Focus line without focus arg: %q", res2.ForLLM)
	}
}
