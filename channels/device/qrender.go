package device

import (
	"encoding/base64"
	"fmt"

	qrcode "github.com/skip2/go-qrcode"
)

// RenderQRCodePNGDataURL renders content (the clawdbot-gateway setup payload JSON)
// as a PNG QR code and returns it as a data: URL suitable for an <img> src.
func RenderQRCodePNGDataURL(content string) (string, error) {
	png, err := qrcode.Encode(content, qrcode.Medium, 512)
	if err != nil {
		return "", fmt.Errorf("device: render qr png: %w", err)
	}
	return "data:image/png;base64," + base64.StdEncoding.EncodeToString(png), nil
}

// RenderQRCodeASCII renders content as a compact half-block ASCII QR code for
// terminals (the headless `claw devices pair` path).
func RenderQRCodeASCII(content string) (string, error) {
	q, err := qrcode.New(content, qrcode.Medium)
	if err != nil {
		return "", fmt.Errorf("device: render qr ascii: %w", err)
	}
	return q.ToSmallString(false), nil
}
