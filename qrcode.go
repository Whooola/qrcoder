package main

import (
	"bytes"
	"fmt"
	"image"
	"image/color"
	"image/png"

	"github.com/makiuchi-d/gozxing"
	"github.com/makiuchi-d/gozxing/qrcode"
	"rsc.io/qr"
)

// QR capacity limits (V40, Level H — highest error correction ~30%)
// In byte mode, V40-H can hold 1273 bytes.
// In alphanumeric mode, V40-H can hold 1852 characters.
const (
	qrMaxBytes        = 1273
	qrMaxAlphanumeric = 1852
	qrTargetSize      = 300 // default pixel size including border
)

// generateQR encodes text as a QR code PNG at Level H (highest error
// correction). Returns PNG bytes, whether text was truncated, and error.
func generateQR(text string) ([]byte, bool, error) {
	truncated := false

	if text == "" {
		return nil, false, fmt.Errorf("文本为空")
	}

	if needsTruncation(text) {
		text = truncateText(text)
		truncated = true
	}

	code, err := qr.Encode(text, qr.H)
	if err != nil {
		return nil, false, fmt.Errorf("二维码编码失败: %w", err)
	}

	return renderQRPNG(code, truncated), nil, nil
}

// needsTruncation checks if text exceeds QR V40-H capacity.
func needsTruncation(text string) bool {
	if len(text) > qrMaxBytes {
		return true
	}
	// Also check rune count for alphanumeric
	runeCount := len([]rune(text))
	return runeCount > qrMaxAlphanumeric
}

// truncateText truncates text to fit within QR V40-H limits.
func truncateText(text string) string {
	runes := []rune(text)
	if len(runes) > qrMaxAlphanumeric {
		runes = runes[:qrMaxAlphanumeric]
	}
	result := string(runes)
	if len(result) > qrMaxBytes {
		result = result[:qrMaxBytes]
	}
	return result
}

// renderQRPNG scales the QR code and renders it as a PNG with white border.
func renderQRPNG(code *qr.Code, truncated bool) []byte {
	// Scale: each QR module becomes 3+ pixels, target ~300px total
	moduleCount := code.Size
	scale := (qrTargetSize + moduleCount - 1) / moduleCount
	if scale < 2 {
		scale = 2
	}

	// 2-module white border
	border := scale * 2
	pixelSize := moduleCount * scale
	imgWidth := pixelSize + border*2
	imgHeight := pixelSize + border*2

	img := image.NewGray(image.Rect(0, 0, imgWidth, imgHeight))

	// Fill white
	for y := 0; y < imgHeight; y++ {
		for x := 0; x < imgWidth; x++ {
			img.SetGray(x, y, color.Gray{Y: 255})
		}
	}

	// Draw QR modules
	for y := 0; y < moduleCount; y++ {
		for x := 0; x < moduleCount; x++ {
			if code.Black(x, y) {
				px := border + x*scale
				py := border + y*scale
				for dy := 0; dy < scale; dy++ {
					for dx := 0; dx < scale; dx++ {
						img.SetGray(px+dx, py+dy, color.Gray{Y: 0})
					}
				}
			}
		}
	}

	var buf bytes.Buffer
	png.Encode(&buf, img)
	return buf.Bytes()
}

// ────────────────────────────────────────────────────────────
// QR detection from camera frame (used by camera.go)
// ────────────────────────────────────────────────────────────

// detectQRFromFrame tries to find and decode a QR code in an RGB24 frame.
// rgbData is raw RGB24 bytes (3 bytes per pixel, row-major).
func detectQRFromFrame(rgbData []byte, width, height int) (string, error) {
	if len(rgbData) < width*height*3 {
		return "", fmt.Errorf("帧数据不足")
	}

	// Convert RGB24 to grayscale image
	gray := image.NewGray(image.Rect(0, 0, width, height))
	for y := 0; y < height; y++ {
		for x := 0; x < width; x++ {
			offset := (y*width + x) * 3
			r := rgbData[offset]
			g := rgbData[offset+1]
			b := rgbData[offset+2]
			// Standard luminance formula
			lum := uint8((int(r)*299 + int(g)*587 + int(b)*114) / 1000)
			gray.SetGray(x, y, color.Gray{Y: lum})
		}
	}

	return detectQRFromImage(gray)
}

// detectQRFromImage attempts to decode a QR code from a grayscale image.
// Uses gozxing for pure-Go QR detection.
func detectQRFromImage(img *image.Gray) (string, error) {
	// Create luminance source from image
	lum := gozxing.NewLuminanceSourceFromImage(img)
	if lum == nil {
		return "", fmt.Errorf("无法从图像创建亮度源")
	}

	// Binarize and create binary bitmap
	binarizer := gozxing.NewHybridBinarizer(lum)
	bitmap, err := gozxing.NewBinaryBitmap(binarizer)
	if err != nil {
		return "", fmt.Errorf("二值化失败: %w", err)
	}

	// Try to decode QR code
	reader := qrcode.NewQRCodeReader()
	result, err := reader.Decode(bitmap, nil)
	if err != nil {
		return "", err // not necessarily an error — just no QR found
	}

	return result.GetText(), nil
}
