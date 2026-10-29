package whatsapplogin

import (
	"bytes"
	"encoding/base64"
	"image/png"
	"strings"
	"testing"
)

// A pairing code as whatsmeow builds it: ref, noise key, identity key and adv
// secret, comma separated. Only its length matters to the symbol size.
var samplePairingCode = "2@" + strings.Repeat("A", 86) + "," + strings.Repeat("B", 44) + "," +
	strings.Repeat("C", 44) + "," + strings.Repeat("D", 44)

func TestQRDataURLDrawsWholePixelModules(t *testing.T) {
	dataURL, err := qrDataURL(samplePairingCode)
	if err != nil {
		t.Fatalf("qrDataURL: %v", err)
	}
	raw, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(dataURL, "data:image/png;base64,"))
	if err != nil {
		t.Fatalf("decode data URL: %v", err)
	}
	img, err := png.Decode(bytes.NewReader(raw))
	if err != nil {
		t.Fatalf("decode png: %v", err)
	}

	width, height := img.Bounds().Dx(), img.Bounds().Dy()
	if width != height {
		t.Fatalf("QR image is %dx%d, want square", width, height)
	}
	// Version 8 (49 modules) plus the 4-module quiet zone on each side. A larger
	// symbol means the error correction level crept back up.
	if want := 57 * qrPixelsPerModule; width != want {
		t.Fatalf("QR image is %dpx wide, want %dpx (57 modules x %dpx)", width, want, qrPixelsPerModule)
	}
}
