package app

import (
	"bytes"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/emersion/go-imap"
	mailmime "github.com/emersion/go-message/mail"
)

func TestMailTextRenderingDoesNotExecuteTerminalControls(t *testing.T) {
	got := htmlText(`<style>hidden</style><p>Hello &amp; goodbye</p><script>secret()</script><p><a href="https://example.com">link</a><img src="https://tracker.example/pixel"></p>`)
	if !strings.Contains(got, "Hello & goodbye") || !strings.Contains(got, "https://example.com") || strings.Contains(got, "hidden") || strings.Contains(got, "secret()") || strings.Contains(got, "tracker") {
		t.Fatal(got)
	}
	if strings.ContainsAny(safeMailText("x\x1b[31m\x07\u202ey"), "\x1b\x07\u202e") {
		t.Fatal("terminal controls escaped sanitization")
	}
}
func TestMimeAlternativePrefersPlainAndSkipsAttachments(t *testing.T) {
	bs := &imap.BodyStructure{MIMEType: "multipart", MIMESubType: "mixed", Parts: []*imap.BodyStructure{
		{MIMEType: "multipart", MIMESubType: "alternative", Parts: []*imap.BodyStructure{{MIMEType: "text", MIMESubType: "plain", Encoding: "quoted-printable", Params: map[string]string{"charset": "utf-8"}}, {MIMEType: "text", MIMESubType: "html"}}},
		{MIMEType: "application", MIMESubType: "pdf", Disposition: "attachment", DispositionParams: map[string]string{"filename": "report.pdf"}},
	}}
	parts, attachments := readableParts(bs)
	if len(parts) != 1 || parts[0].Type != "text/plain" || len(attachments) != 1 {
		t.Fatal(parts, attachments)
	}
	if decodeTextPart(parts[0], []byte("caf=C3=A9")) != "café" {
		t.Fatal("quoted-printable charset decoding failed")
	}
}
func TestWireRoundTripAndHeaderInjection(t *testing.T) {
	c := credentialConfig(t)
	o := outgoingMail{ID: "<test@hane.local>", To: "Friend <friend@example.com>, second@example.com", Subject: "日本語 subject", Body: "Unicode café\nLine two\n.dot", ReplyID: "<parent@example.com>", References: []string{"<root@example.com>", "<parent@example.com>"}, Date: time.Now()}
	wire, targets, err := makeWire(c, o)
	if err != nil {
		t.Fatal(err)
	}
	if len(targets) != 2 {
		t.Fatal("missing SMTP recipients")
	}
	reader, err := mailmime.CreateReader(bytes.NewReader(wire))
	if err != nil {
		t.Fatal(err)
	}
	subject, _ := reader.Header.Subject()
	if subject != o.Subject {
		t.Fatal(subject)
	}
	part, err := reader.NextPart()
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(part.Body)
	if strings.ReplaceAll(string(body), "\r\n", "\n") != o.Body {
		t.Fatalf("body roundtrip: %q", body)
	}
	o.Subject = "Subject\r\nBcc: attacker@example.com"
	if _, _, err := makeWire(c, o); err == nil {
		t.Fatal("header injection accepted")
	}
}
