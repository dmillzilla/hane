// Package mailimage loads bounded raster images and renders terminal previews.
package mailimage

import (
	"bytes"
	"encoding/base64"
	"errors"
	"image"
	"image/color"
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"
	"mime"
	"strings"

	"golang.org/x/image/draw"
	_ "golang.org/x/image/webp"
)

const MaxBytes = 8 << 20
const maxPixels = 16_000_000
const previewSize = 512

// Decode checks dimensions before allocating the full image. GIF previews use
// the first frame. The returned raster is reduced to bound session-cache memory.
func Decode(data []byte, declared string) (image.Image, error) {
	if len(data) == 0 || len(data) > MaxBytes {
		return nil, errors.New("image is empty or larger than 8 MiB")
	}
	config, format, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil || config.Width <= 0 || config.Height <= 0 || config.Width > maxPixels/config.Height {
		return nil, errors.New("image is invalid, unsupported, or larger than 16 megapixels")
	}
	types := map[string]string{"png": "image/png", "jpeg": "image/jpeg", "gif": "image/gif", "webp": "image/webp"}
	want, ok := types[format]
	if !ok {
		return nil, errors.New("only PNG, JPEG, GIF, and WebP images can be displayed")
	}
	if declared != "" {
		kind, _, err := mime.ParseMediaType(declared)
		if err != nil {
			return nil, errors.New("invalid image content type")
		}
		if kind == "image/jpg" {
			kind = "image/jpeg"
		}
		if kind != want && kind != "application/octet-stream" {
			return nil, errors.New("image content does not match its declared type")
		}
	}
	if config.Width <= 2 || config.Height <= 2 {
		return nil, errors.New("tiny tracking-sized images are not displayed")
	}
	source, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return nil, errors.New("could not decode image")
	}
	w, h := fitSize(config.Width, config.Height, previewSize, previewSize)
	preview := image.NewRGBA(image.Rect(0, 0, w, h))
	draw.ApproxBiLinear.Scale(preview, preview.Bounds(), source, source.Bounds(), draw.Src, nil)
	return preview, nil
}

// Data decodes only explicit base64 raster data URIs, never SVG or HTML.
func Data(source string) ([]byte, string, error) {
	header, encoded, ok := strings.Cut(source, ",")
	if !ok || !strings.HasPrefix(strings.ToLower(header), "data:image/") || !strings.HasSuffix(strings.ToLower(header), ";base64") || len(encoded) > base64.StdEncoding.EncodedLen(MaxBytes) {
		return nil, "", errors.New("unsupported embedded image")
	}
	kind := strings.TrimSuffix(strings.TrimPrefix(strings.ToLower(header), "data:"), ";base64")
	switch kind {
	case "image/png", "image/jpeg", "image/gif", "image/webp":
	default:
		return nil, "", errors.New("unsupported embedded image type")
	}
	data, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return nil, "", errors.New("invalid embedded image")
	}
	return data, kind, nil
}

func fitSize(w, h, maxW, maxH int) (int, int) {
	if w > maxW {
		h, w = max(1, h*maxW/w), maxW
	}
	if h > maxH {
		w, h = max(1, w*maxH/h), maxH
	}
	return max(1, w), max(1, h)
}

// Lines uses truecolor half-block cells. It participates in the TUI's normal
// clipping and redraws, so images scroll correctly without terminal extensions.
func Lines(source image.Image, width, maxRows int, background color.Color) []string {
	if source == nil || width < 1 || maxRows < 1 {
		return nil
	}
	bounds := source.Bounds()
	if bounds.Empty() {
		return nil
	}
	w, h := fitSize(bounds.Dx(), bounds.Dy(), min(width, 100), maxRows*2)
	scaled := image.NewRGBA(image.Rect(0, 0, w, h))
	draw.ApproxBiLinear.Scale(scaled, scaled.Bounds(), source, bounds, draw.Src, nil)
	var lines []string
	for y := 0; y < h; y += 2 {
		var line strings.Builder
		for x := 0; x < w; x++ {
			top := over(scaled.At(x, y), background)
			bottom := over(color.Transparent, background)
			if y+1 < h {
				bottom = over(scaled.At(x, y+1), background)
			}
			// Only application-generated colour escapes enter the output.
			line.WriteString(escapeColor(top, bottom))
			line.WriteRune('▀')
		}
		line.WriteString("\x1b[0m")
		lines = append(lines, line.String())
	}
	return lines
}

func over(fg, bg color.Color) color.RGBA {
	r, g, b, a := fg.RGBA()
	br, bgc, bb, _ := bg.RGBA()
	return color.RGBA{uint8((r + br*(65535-a)/65535) >> 8), uint8((g + bgc*(65535-a)/65535) >> 8), uint8((b + bb*(65535-a)/65535) >> 8), 255}
}
