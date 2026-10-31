package mailimage

import (
	"bytes"
	"encoding/base64"
	"encoding/binary"
	"hash/crc32"
	"image"
	"image/color"
	"image/gif"
	"image/jpeg"
	"image/png"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
)

func pngFixture(t *testing.T) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 12, 8))
	for y := 0; y < 8; y++ {
		for x := 0; x < 12; x++ {
			img.SetRGBA(x, y, color.RGBA{uint8(x * 20), uint8(y * 30), 170, 255})
		}
	}
	var b bytes.Buffer
	if err := png.Encode(&b, img); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

func TestDecodeSupportedRasterAndBoundedPreview(t *testing.T) {
	source, _, err := image.Decode(bytes.NewReader(pngFixture(t)))
	if err != nil {
		t.Fatal(err)
	}
	for _, kind := range []string{"png", "jpeg", "gif"} {
		t.Run(kind, func(t *testing.T) {
			var data bytes.Buffer
			switch kind {
			case "png":
				err = png.Encode(&data, source)
			case "jpeg":
				err = jpeg.Encode(&data, source, nil)
			case "gif":
				err = gif.Encode(&data, source, nil)
			}
			if err != nil {
				t.Fatal(err)
			}
			img, err := Decode(data.Bytes(), "image/"+kind)
			if err != nil || img.Bounds().Dx() != 12 || img.Bounds().Dy() != 8 {
				t.Fatalf("decode %s: %v", kind, err)
			}
			for _, size := range [][2]int{{1, 1}, {8, 2}, {40, 20}} {
				lines := Lines(img, size[0], size[1], color.RGBA{11, 22, 43, 255})
				if len(lines) == 0 || len(lines) > size[1] {
					t.Fatal("image exceeded row budget")
				}
				for _, line := range lines {
					if ansi.StringWidth(line) > size[0] || !strings.Contains(line, "▀") {
						t.Fatal("image exceeded column budget or did not render pixels")
					}
				}
			}
		})
	}
}

func TestRefuseInvalidSpoofedTinyAndHugeImages(t *testing.T) {
	// A valid IHDR is enough to read dimensions; no large pixel allocation is
	// needed to verify that a malicious decompression size is rejected.
	var large bytes.Buffer
	large.WriteString("\x89PNG\r\n\x1a\n")
	header := make([]byte, 13)
	binary.BigEndian.PutUint32(header, 5000)
	binary.BigEndian.PutUint32(header[4:], 5000)
	header[8], header[9] = 8, 2
	_ = binary.Write(&large, binary.BigEndian, uint32(len(header)))
	large.WriteString("IHDR")
	large.Write(header)
	_ = binary.Write(&large, binary.BigEndian, crc32.ChecksumIEEE(append([]byte("IHDR"), header...)))
	var tiny bytes.Buffer
	_ = png.Encode(&tiny, image.NewRGBA(image.Rect(0, 0, 1, 1)))
	for _, tc := range []struct {
		data []byte
		kind string
	}{
		{[]byte(`<svg><script>bad()</script></svg>`), "image/png"},
		{pngFixture(t), "image/svg+xml"},
		{pngFixture(t), "image/jpeg"},
		{large.Bytes(), "image/png"},
		{tiny.Bytes(), "image/png"},
		{nil, "image/png"},
	} {
		if _, err := Decode(tc.data, tc.kind); err == nil {
			t.Fatal("unsafe or invalid image was accepted")
		}
	}
}

func TestEmbeddedRasterDataIsValidated(t *testing.T) {
	raw := pngFixture(t)
	source := "data:image/png;base64," + base64.StdEncoding.EncodeToString(raw)
	decoded, kind, err := Data(source)
	if err != nil || kind != "image/png" || !bytes.Equal(raw, decoded) {
		t.Fatal("valid embedded PNG was not decoded")
	}
	for _, source := range []string{"data:text/html;base64,AAAA", "data:image/svg+xml;base64,AAAA", "data:image/png;base64,not base64", "file:///tmp/image.png"} {
		if _, _, err := Data(source); err == nil {
			t.Fatal("invalid data URI accepted")
		}
	}
}

func TestTransparentPixelsUseReaderBackground(t *testing.T) {
	got := over(color.Transparent, color.RGBA{11, 22, 43, 255})
	if got != (color.RGBA{11, 22, 43, 255}) {
		t.Fatal("transparent pixels did not use the reader background")
	}
}

func TestWebPPreview(t *testing.T) {
	// A locally generated 16x12 solid-colour lossless WebP fixture.
	data, err := base64.StdEncoding.DecodeString("UklGRh4AAABXRUJQVlA4TBEAAAAvD8ACAAdQ2lIUuf+BiOh/AAA=")
	if err != nil {
		t.Fatal(err)
	}
	img, err := Decode(data, "image/webp")
	if err != nil || img.Bounds().Dx() != 16 || img.Bounds().Dy() != 12 {
		t.Fatalf("WebP decode failed: %v", err)
	}
}
