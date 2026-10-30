package main

import (
	"bytes"
	"fmt"
	"io"
	"mime"
	"net/mail"
	"regexp"
	"strings"
	"unicode"

	"github.com/emersion/go-imap"
	message "github.com/emersion/go-message"
	_ "github.com/emersion/go-message/charset"
	mailmime "github.com/emersion/go-message/mail"
	"golang.org/x/net/html"
)

var messageIDPattern = regexp.MustCompile(`<[^<>\s\x00-\x1f]+>`)

func ids(s string) []string { return messageIDPattern.FindAllString(s, 1000) }
func safeMailText(s string) string {
	return strings.Map(func(r rune) rune {
		if r == '\n' || r == '\t' {
			return r
		}
		if unicode.IsControl(r) || (r >= 0x202a && r <= 0x202e) || (r >= 0x2066 && r <= 0x2069) {
			return -1
		}
		return r
	}, strings.ToValidUTF8(strings.ReplaceAll(s, "\r\n", "\n"), "�"))
}
func htmlText(s string) string {
	n, err := html.Parse(strings.NewReader(s))
	if err != nil {
		return "[Could not render HTML message]"
	}
	var b strings.Builder
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.ElementNode {
			switch n.Data {
			case "script", "style", "head", "svg", "noscript":
				return
			case "p", "div", "br", "tr", "li", "blockquote", "h1", "h2", "h3":
				b.WriteByte('\n')
			}
		}
		if n.Type == html.TextNode {
			b.WriteString(n.Data)
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
		if n.Type == html.ElementNode && n.Data == "a" {
			for _, a := range n.Attr {
				if a.Key == "href" && (strings.HasPrefix(a.Val, "https://") || strings.HasPrefix(a.Val, "http://")) {
					b.WriteString(" (" + a.Val + ")")
				}
			}
		}
	}
	walk(n)
	var lines []string
	for _, line := range strings.Split(safeMailText(b.String()), "\n") {
		line = strings.Join(strings.Fields(line), " ")
		if line != "" {
			lines = append(lines, line)
		}
	}
	return strings.Join(lines, "\n")
}
func applyHeader(r *remoteMail, raw []byte) {
	reader, _ := mailmime.CreateReader(bytes.NewReader(raw))
	if reader == nil {
		return
	}
	h := reader.Header
	if s, e := h.Subject(); e == nil {
		r.Subject = safeMailText(s)
	}
	r.MessageID = ""
	if v := ids(h.Get("Message-ID")); len(v) > 0 {
		r.MessageID = v[0]
	}
	if v := ids(h.Get("In-Reply-To")); len(v) > 0 {
		r.InReplyTo = v[0]
	}
	r.References = ids(h.Get("References"))
	for _, field := range []string{"From", "To", "Reply-To"} {
		a, e := h.AddressList(field)
		if e != nil || len(a) == 0 {
			continue
		}
		switch field {
		case "From":
			r.From = a[0].Address
			r.Sender = safeMailText(a[0].Name)
		case "To":
			var ss []string
			for _, a := range a {
				ss = append(ss, a.String())
			}
			r.To = strings.Join(ss, ", ")
		case "Reply-To":
			var ss []string
			for _, a := range a {
				ss = append(ss, a.String())
			}
			r.ReplyAddress = strings.Join(ss, ", ")
		}
	}
	if r.Subject == "" {
		r.Subject = "(no subject)"
	}
	if r.Sender == "" {
		r.Sender = safeMailText(r.From)
	}
}

type textPart struct {
	Path           []int
	Type, Encoding string
	Params         map[string]string
	Size           uint32
}

func readableParts(bs *imap.BodyStructure) ([]textPart, []string) {
	var visit func(*imap.BodyStructure, []int) ([]textPart, []string)
	visit = func(p *imap.BodyStructure, path []int) ([]textPart, []string) {
		if p == nil {
			return nil, nil
		}
		if strings.EqualFold(p.Disposition, "attachment") || (p.MIMEType != "multipart" && p.MIMEType != "text") {
			name := p.DispositionParams["filename"]
			if name == "" {
				name = p.Params["name"]
			}
			if name == "" {
				name = p.MIMEType + "/" + p.MIMESubType
			}
			return nil, []string{safeMailText(name)}
		}
		if strings.EqualFold(p.MIMEType, "multipart") {
			var out []textPart
			var attachments []string
			for i, c := range p.Parts {
				parts, a := visit(c, append(append([]int{}, path...), i+1))
				attachments = append(attachments, a...)
				out = append(out, parts...)
			}
			if strings.EqualFold(p.MIMESubType, "alternative") {
				for _, p := range out {
					if p.Type == "text/plain" {
						return []textPart{p}, attachments
					}
				}
				if len(out) > 0 {
					return out[:1], attachments
				}
			}
			return out, attachments
		}
		if strings.EqualFold(p.MIMEType, "text") && (strings.EqualFold(p.MIMESubType, "plain") || strings.EqualFold(p.MIMESubType, "html")) {
			if len(path) == 0 {
				path = []int{1}
			}
			return []textPart{{path, "text/" + strings.ToLower(p.MIMESubType), p.Encoding, p.Params, p.Size}}, nil
		}
		return nil, nil
	}
	return visit(bs, nil)
}
func decodeTextPart(p textPart, data []byte) string {
	var h message.Header
	h.Set("Content-Type", mime.FormatMediaType(p.Type, p.Params))
	h.Set("Content-Transfer-Encoding", p.Encoding)
	e, err := message.New(h, bytes.NewReader(data))
	if e == nil {
		return "[Could not decode this message part]"
	}
	content, readErr := io.ReadAll(io.LimitReader(e.Body, 2<<20))
	body := string(content)
	if p.Type == "text/html" {
		body = htmlText(body)
	}
	body = safeMailText(body)
	if err != nil || readErr != nil {
		body += "\n[Some text could not be decoded.]"
	}
	return body
}
func makeWire(c AccountConfig, o outgoingMail) ([]byte, []string, error) {
	if strings.ContainsAny(o.To+o.Subject, "\r\n\x00") {
		return nil, nil, fmt.Errorf("Recipients and subject must each be a single line.")
	}
	recipients, err := mail.ParseAddressList(o.To)
	if err != nil || len(recipients) == 0 {
		return nil, nil, fmt.Errorf("Set valid recipients with :to address@example.com")
	}
	if strings.TrimSpace(o.Subject) == "" {
		return nil, nil, fmt.Errorf("Set a subject with :subject Your subject")
	}
	if strings.TrimSpace(o.Body) == "" {
		return nil, nil, fmt.Errorf("Add a message before sending.")
	}
	var targets []string
	for _, a := range recipients {
		if !strings.Contains(a.Address, "@") || !safeField(a.Address) {
			return nil, nil, fmt.Errorf("Invalid recipient.")
		}
		targets = append(targets, a.Address)
	}
	var h mailmime.Header
	h.SetAddressList("From", []*mail.Address{{Name: c.Name, Address: c.Email}})
	h.SetAddressList("To", recipients)
	h.SetSubject(o.Subject)
	h.SetDate(o.Date)
	h.Set("Message-ID", o.ID)
	h.Set("MIME-Version", "1.0")
	if v := ids(o.ReplyID); len(v) == 1 {
		h.Set("In-Reply-To", v[0])
	}
	var refs []string
	for _, s := range o.References {
		if v := ids(s); len(v) == 1 {
			refs = append(refs, v[0])
		}
	}
	if len(refs) > 40 {
		refs = append(refs[:1], refs[len(refs)-39:]...)
	}
	if len(refs) > 0 {
		h.Set("References", strings.Join(refs, " "))
	}
	h.Set("Content-Type", "text/plain; charset=utf-8")
	h.Set("Content-Transfer-Encoding", "quoted-printable")
	var b bytes.Buffer
	w, err := mailmime.CreateSingleInlineWriter(&b, h)
	if err != nil {
		return nil, nil, err
	}
	if _, err = w.Write([]byte(o.Body)); err != nil {
		return nil, nil, err
	}
	if err = w.Close(); err != nil {
		return nil, nil, err
	}
	return b.Bytes(), targets, nil
}
