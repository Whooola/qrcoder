package main

import (
	"bytes"
	"image/png"
	"strings"
	"testing"
)

func TestGenerateQR_ShortASCII(t *testing.T) {
	pngBytes, truncated, err := generateQR("https://example.com")
	if err != nil {
		t.Fatalf("generateQR failed: %v", err)
	}
	if truncated {
		t.Error("short text should not be truncated")
	}
	if len(pngBytes) == 0 {
		t.Error("expected non-empty PNG bytes")
	}

	// Verify it's a valid PNG
	_, err = png.Decode(bytes.NewReader(pngBytes))
	if err != nil {
		t.Errorf("output is not valid PNG: %v", err)
	}
}

func TestGenerateQR_LongText(t *testing.T) {
	// Create text that exceeds QR capacity
	longText := strings.Repeat("A", qrMaxAlphanumeric+100)
	_, truncated, err := generateQR(longText)
	if err != nil {
		t.Fatalf("generateQR failed on long text: %v", err)
	}
	if !truncated {
		t.Error("long text should be marked as truncated")
	}
}

func TestGenerateQR_Empty(t *testing.T) {
	_, _, err := generateQR("")
	if err == nil {
		t.Error("empty text should return error")
	}
}

func TestGenerateQR_Unicode(t *testing.T) {
	pngBytes, truncated, err := generateQR("你好世界 Hello World 123")
	if err != nil {
		t.Fatalf("generateQR failed on unicode: %v", err)
	}
	if truncated {
		t.Error("short unicode text should not be truncated")
	}
	if len(pngBytes) == 0 {
		t.Error("expected non-empty PNG bytes")
	}
}

func TestNeedsTruncation_Short(t *testing.T) {
	if needsTruncation("hello") {
		t.Error("short text should not need truncation")
	}
}

func TestNeedsTruncation_Long(t *testing.T) {
	longText := strings.Repeat("A", qrMaxBytes+50)
	if !needsTruncation(longText) {
		t.Error("long text should need truncation")
	}
}

func TestTruncateText(t *testing.T) {
	longText := strings.Repeat("A", qrMaxAlphanumeric+200)
	truncated := truncateText(longText)
	runes := []rune(truncated)
	if len(runes) > qrMaxAlphanumeric {
		t.Errorf("truncated text has %d runes, max is %d", len(runes), qrMaxAlphanumeric)
	}
	if len(truncated) > qrMaxBytes {
		t.Errorf("truncated text has %d bytes, max is %d", len(truncated), qrMaxBytes)
	}
}

func TestGenerateQR_PNG_Dimensions(t *testing.T) {
	pngBytes, _, err := generateQR("test QR code data")
	if err != nil {
		t.Fatalf("generateQR failed: %v", err)
	}
	img, err := png.Decode(bytes.NewReader(pngBytes))
	if err != nil {
		t.Fatalf("decode PNG: %v", err)
	}
	bounds := img.Bounds()
	width := bounds.Dx()
	height := bounds.Dy()

	// Should be square
	if width != height {
		t.Errorf("QR image not square: %dx%d", width, height)
	}

	// Should be at least 150px
	if width < 150 {
		t.Errorf("QR image too small: %dx%d", width, height)
	}
}
