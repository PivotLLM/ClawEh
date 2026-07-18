// ClawEh
// License: MIT

package files

import (
	"bytes"
	"context"
	"encoding/base64"
	"fmt"
	"image"
	"image/jpeg"
	"image/png"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/PivotLLM/ClawEh/pkg/global"
	"github.com/PivotLLM/ClawEh/pkg/tools"

	"github.com/h2non/filetype"
	xdraw "golang.org/x/image/draw"

	// Register image decoders. png/jpeg/gif are stdlib; webp comes from x/image.
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"

	_ "golang.org/x/image/webp"
)

// MaxViewImageSize caps the source image file file_view_image will load, in
// bytes. Larger files are refused (base64 + vision tokens for a huge image are
// prohibitive; the image should be shrunk on disk first).
const MaxViewImageSize = 10 * 1024 * 1024 // 10 MB

// ViewImageTool loads an image file from the (sandboxed) workspace and returns
// it for a vision-capable model to look at. Oversized images are downscaled so
// the longest edge is global.ImageDownscaleMaxEdgePx.
type ViewImageTool struct {
	sysFs   fileSystem
	maxSize int64
}

// NewViewImageTool builds the file_view_image tool over the same sandbox as the
// read tools.
func NewViewImageTool(workspace string, restrict bool, allowPaths ...[]*regexp.Regexp) *ViewImageTool {
	var patterns []*regexp.Regexp
	if len(allowPaths) > 0 {
		patterns = allowPaths[0]
	}
	return &ViewImageTool{
		sysFs:   buildFs(workspace, restrict, patterns),
		maxSize: MaxViewImageSize,
	}
}

func (t *ViewImageTool) Name() string { return "view_image" }

func (t *ViewImageTool) Description() string {
	return "View an image file so you can actually see it (requires a vision-capable model). " +
		"Reads an image from the workspace (png, jpeg, gif, webp) and attaches it for you to look at — " +
		"use this instead of file_read_lines/file_read_bytes, which return unusable binary for images. " +
		"Large images are automatically downscaled. Max source size 10 MB."
}

func (t *ViewImageTool) Parameters() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"path": map[string]any{
				"type":        "string",
				"description": "Path to the image file, relative to the workspace (or an allowed absolute path).",
			},
		},
		"required": []string{"path"},
	}
}

func (t *ViewImageTool) Execute(_ context.Context, args map[string]any) *tools.ToolResult {
	path, ok := args["path"].(string)
	if !ok || strings.TrimSpace(path) == "" {
		return tools.ErrorResult("path is required")
	}

	data, err := t.sysFs.ReadFile(path)
	if err != nil {
		return tools.ErrorResult(fmt.Sprintf("failed to read %q: %v", path, err))
	}
	if int64(len(data)) > t.maxSize {
		return tools.ErrorResult(fmt.Sprintf(
			"image is %.1f MB; the limit is %d MB — shrink it first",
			float64(len(data))/(1024*1024), t.maxSize/(1024*1024)))
	}

	kind, _ := filetype.Match(data)
	if kind == filetype.Unknown || !strings.HasPrefix(kind.MIME.Value, "image/") {
		detected := "unknown"
		if kind != filetype.Unknown {
			detected = kind.MIME.Value
		}
		return tools.ErrorResult(fmt.Sprintf(
			"%q is not an image (detected %s); file_view_image only handles images", path, detected))
	}

	dataURL, w, h, note := imageToDataURL(data, kind.MIME.Value)
	return &tools.ToolResult{
		ForLLM: fmt.Sprintf("[viewing image %s — %d×%d%s]", filepath.Base(path), w, h, note),
		Images: []string{dataURL},
	}
}

// imageToDataURL returns a data: URL for the image, downscaling to
// global.ImageDownscaleMaxEdgePx on the longest edge when larger. On any decode
// failure it falls back to the original bytes with their detected MIME (the
// model's provider may still handle them). note describes what was done.
func imageToDataURL(data []byte, mime string) (url string, width, height int, note string) {
	src, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		// Could not decode locally (e.g. an exotic format) — pass through as-is.
		return "data:" + mime + ";base64," + base64.StdEncoding.EncodeToString(data), 0, 0, " (passed through)"
	}
	b := src.Bounds()
	w, h := b.Dx(), b.Dy()

	longest := w
	if h > w {
		longest = h
	}
	if longest <= global.ImageDownscaleMaxEdgePx {
		// No downscale needed — send the original bytes untouched.
		return "data:" + mime + ";base64," + base64.StdEncoding.EncodeToString(data), w, h, ""
	}

	// Downscale so the longest edge == the threshold, preserving aspect ratio.
	scale := float64(global.ImageDownscaleMaxEdgePx) / float64(longest)
	nw, nh := int(float64(w)*scale), int(float64(h)*scale)
	if nw < 1 {
		nw = 1
	}
	if nh < 1 {
		nh = 1
	}
	dst := image.NewRGBA(image.Rect(0, 0, nw, nh))
	xdraw.CatmullRom.Scale(dst, dst.Bounds(), src, b, xdraw.Over, nil)

	// Re-encode: keep PNG for sources that may have alpha; JPEG otherwise (smaller).
	var buf bytes.Buffer
	outMime := "image/jpeg"
	if mime == "image/png" || mime == "image/gif" {
		outMime = "image/png"
		if encErr := png.Encode(&buf, dst); encErr != nil {
			return "data:" + mime + ";base64," + base64.StdEncoding.EncodeToString(data), w, h, ""
		}
	} else if encErr := jpeg.Encode(&buf, dst, &jpeg.Options{Quality: 85}); encErr != nil {
		return "data:" + mime + ";base64," + base64.StdEncoding.EncodeToString(data), w, h, ""
	}

	return "data:" + outMime + ";base64," + base64.StdEncoding.EncodeToString(buf.Bytes()),
		nw, nh, fmt.Sprintf(" (downscaled from %d×%d)", w, h)
}
