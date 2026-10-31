package app

import (
	"bytes"
	"context"
	"encoding/base64"
	"image"
	"image/png"
	"strings"
	"testing"
	"time"

	"github.com/dmillzilla/hane/internal/mailimage"
)

func imagePNG(t *testing.T) []byte {
	t.Helper()
	var data bytes.Buffer
	if err := png.Encode(&data, image.NewRGBA(image.Rect(0, 0, 16, 12))); err != nil {
		t.Fatal(err)
	}
	return data.Bytes()
}

func TestIMAPIndexesHTMLImagesAndFetchesCIDWithPeek(t *testing.T) {
	b, c := liveIMAPFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	imageBytes := imagePNG(t)
	raw := "From: Photos <photos@example.com>\r\nTo: reader@example.com\r\nSubject: Photos\r\nMessage-ID: <images@example.com>\r\nMIME-Version: 1.0\r\nContent-Type: multipart/mixed; boundary=outer\r\n\r\n" +
		"--outer\r\nContent-Type: multipart/alternative; boundary=alt\r\n\r\n" +
		"--alt\r\nContent-Type: text/plain; charset=utf-8\r\n\r\nPlain text stays available\r\n" +
		"--alt\r\nContent-Type: text/html; charset=utf-8\r\n\r\n<p>Before photo</p><img src=\"cid:coast@example.com\" alt=\"Coast\"><p>After photo</p><img src=\"https://images.example/remote.png\" alt=\"Remote\"><img src=\"https://images.example/pixel\" width=\"1\" height=\"1\">\r\n--alt--\r\n" +
		"--outer\r\nContent-Type: image/png; name=coast.png\r\nContent-ID: <coast@example.com>\r\nContent-Disposition: inline; filename=coast.png\r\nContent-Transfer-Encoding: base64\r\n\r\n" + base64.StdEncoding.EncodeToString(imageBytes) + "\r\n" +
		"--outer\r\nContent-Type: image/png; name=attachment.png\r\nContent-Disposition: attachment; filename=attachment.png\r\nContent-Transfer-Encoding: base64\r\n\r\n" + base64.StdEncoding.EncodeToString(imageBytes) + "\r\n--outer--\r\n"
	im, close, err := b.connect(ctx, c, "password")
	if err != nil {
		t.Fatal(err)
	}
	if err := im.Append("INBOX", nil, time.Now(), bytes.NewReader([]byte(raw))); err != nil {
		close()
		t.Fatal(err)
	}
	close()
	result := b.Sync(ctx, c, "password", accountMail{}, syncRequest{Folder: "INBOX"})
	if result.Err != nil {
		t.Fatal(result.Err)
	}
	var message remoteMail
	for _, r := range result.Data.Messages {
		if r.MessageID == "<images@example.com>" {
			message = r
		}
	}
	if !message.ImagesIndexed || len(message.Images) != 3 || message.Seen || !strings.Contains(message.Body, "Plain text stays available") || strings.Contains(message.Body, "Before photo") {
		t.Fatalf("plain text, image metadata, or read status was lost: %+v", message)
	}
	if !strings.Contains(message.ImageBody, "Before photo") || !strings.Contains(message.ImageBody, "After photo") || strings.Contains(message.ImageBody, "pixel") {
		t.Fatal("HTML image placement or tracking-image suppression failed")
	}
	var cid messageImage
	for _, img := range message.Images {
		if img.CID == "coast@example.com" {
			cid = img
		}
	}
	data, kind, err := b.Image(ctx, c, "password", message.Ref, cid, mailimage.MaxBytes)
	if err != nil || kind != "image/png" || !bytes.Equal(data, imageBytes) {
		t.Fatalf("image MIME part did not decode: %v", err)
	}
	if _, err := mailimage.Decode(data, kind); err != nil {
		t.Fatal(err)
	}
	stale := message.Ref
	stale.Validity++
	if _, _, err := b.Image(ctx, c, "password", stale, cid, mailimage.MaxBytes); err == nil || !strings.Contains(err.Error(), "mailbox changed") {
		t.Fatal("image fetch did not reject stale UIDVALIDITY")
	}
	for i := range result.Data.Messages {
		if result.Data.Messages[i].MessageID == message.MessageID {
			result.Data.Messages[i].ImagesIndexed = false
			result.Data.Messages[i].Images = nil
			result.Data.Messages[i].ImageBody = ""
		}
	}
	refreshed := b.Sync(ctx, c, "password", result.Data, syncRequest{Folder: "INBOX"})
	if refreshed.Err != nil {
		t.Fatal(refreshed.Err)
	}
	for _, r := range refreshed.Data.Messages {
		if r.MessageID == message.MessageID && (r.Seen || !r.ImagesIndexed || len(r.Images) != 3) {
			t.Fatalf("BODY.PEEK/cache refresh: seen=%v indexed=%v images=%d", r.Seen, r.ImagesIndexed, len(r.Images))
		}
	}
}
