package app

import (
	"crypto/sha256"
	"fmt"
	"net/url"
	"strconv"
	"strings"

	"github.com/dmillzilla/hane/internal/mailimage"
	"github.com/emersion/go-imap"
	"golang.org/x/net/html"
)

const maxMessageImages = 32

// Only image references are persisted with mail. Downloaded pixels stay in the
// bounded session cache and cannot be fetched by the text renderer itself.
type messageImage struct {
	ID, Name, Source, MIMEType, Encoding, CID string
	Part                                      []int
	Size                                      uint32
	Attachment                                bool
}

type messageImages struct {
	Text   string
	Images []messageImage
	Ref    mailRef
}

func (i messageImage) token() string { return "[[hane-image:" + i.ID + "]]" }

func imageID(source string) string {
	sum := sha256.Sum256([]byte(source))
	return fmt.Sprintf("%x", sum[:8])
}

func imageName(s string) string {
	s = strings.Join(strings.Fields(safeMailText(s)), " ")
	r := []rune(s)
	if len(r) > 80 {
		s = string(r[:80]) + "…"
	}
	if s == "" {
		return "Image"
	}
	return s
}

func supportedImage(kind string) bool {
	switch strings.ToLower(kind) {
	case "image/png", "image/jpeg", "image/jpg", "image/gif", "image/webp":
		return true
	}
	return false
}

// imageMIMEParts also finds an HTML alternative even when the default reader
// prefers plain text. Walking is bounded independently of sender MIME nesting.
func imageMIMEParts(bs *imap.BodyStructure) ([]messageImage, *textPart) {
	var images []messageImage
	var htmlPart *textPart
	visited := 0
	var walk func(*imap.BodyStructure, []int, int)
	walk = func(p *imap.BodyStructure, path []int, depth int) {
		if p == nil || depth > 24 || visited >= 256 {
			return
		}
		visited++
		attached := strings.EqualFold(p.Disposition, "attachment")
		if strings.EqualFold(p.MIMEType, "multipart") {
			if attached {
				return
			}
			for j, child := range p.Parts {
				walk(child, append(append([]int(nil), path...), j+1), depth+1)
			}
			return
		}
		if len(path) == 0 {
			path = []int{1}
		}
		kind := strings.ToLower(p.MIMEType + "/" + p.MIMESubType)
		if kind == "text/html" && !attached && htmlPart == nil {
			htmlPart = &textPart{Path: path, Type: kind, Encoding: p.Encoding, Params: p.Params, Size: p.Size}
		}
		if !supportedImage(kind) || len(images) >= maxMessageImages {
			return
		}
		name := p.DispositionParams["filename"]
		if name == "" {
			name = p.Params["name"]
		}
		if name == "" {
			name = kind
		}
		images = append(images, messageImage{
			ID: imageID(fmt.Sprint(path)), Name: imageName(name), Part: path,
			MIMEType: kind, Encoding: p.Encoding, Size: p.Size,
			CID: strings.Trim(strings.TrimSpace(p.Id), "<>"), Attachment: attached,
		})
	}
	walk(bs, nil, 0)
	return images, htmlPart
}

func imageHidden(n *html.Node) bool {
	for _, a := range n.Attr {
		value := strings.ToLower(strings.Join(strings.Fields(a.Val), ""))
		switch a.Key {
		case "hidden":
			return true
		case "aria-hidden":
			if value == "true" {
				return true
			}
		case "style":
			for _, declaration := range strings.Split(value, ";") {
				property, setting, _ := strings.Cut(declaration, ":")
				setting = strings.TrimSuffix(setting, "!important")
				if property == "display" && setting == "none" || property == "visibility" && setting == "hidden" {
					return true
				}
				if property == "opacity" {
					if opacity, err := strconv.ParseFloat(setting, 64); err == nil && opacity == 0 {
						return true
					}
				}
				if n.Data == "img" && (property == "width" || property == "height") {
					if pixels, err := strconv.ParseFloat(strings.TrimSuffix(setting, "px"), 64); err == nil && pixels >= 0 && pixels <= 2 {
						return true
					}
				}
			}
		case "width", "height":
			if n.Data == "img" {
				pixels, err := strconv.Atoi(strings.TrimSuffix(value, "px"))
				if err == nil && pixels >= 0 && pixels <= 2 {
					return true
				}
			}
		}
	}
	return false
}

func htmlImages(source string, parts []messageImage) (string, []messageImage) {
	root, err := html.Parse(strings.NewReader(source))
	if err != nil {
		return "", nil
	}
	cids := map[string]messageImage{}
	for _, part := range parts {
		if part.CID != "" {
			cids[part.CID] = part
		}
	}
	var text strings.Builder
	var images []messageImage
	seen := map[string]bool{}
	var walk func(*html.Node, int)
	walk = func(n *html.Node, depth int) {
		if depth > 128 {
			return
		}
		if n.Type == html.ElementNode {
			if imageHidden(n) {
				return
			}
			switch n.Data {
			case "script", "style", "head", "svg", "noscript", "iframe", "object", "embed":
				return
			case "p", "div", "br", "tr", "li", "blockquote", "h1", "h2", "h3":
				text.WriteByte('\n')
			case "img":
				if len(images) >= maxMessageImages {
					return
				}
				var src, alt string
				for _, a := range n.Attr {
					if a.Key == "src" {
						src = strings.TrimSpace(a.Val)
						if strings.HasPrefix(src, "//") {
							src = "https:" + src
						}
					} else if a.Key == "alt" {
						alt = imageName(a.Val)
					}
				}
				img := messageImage{ID: imageID(src), Name: imageName(alt), Source: src}
				switch {
				case strings.HasPrefix(strings.ToLower(src), "cid:"):
					cid, _ := url.PathUnescape(src[4:])
					part, ok := cids[strings.Trim(cid, "<>")]
					if !ok {
						return
					}
					img = part
					if alt != "" {
						img.Name = alt
					}
				case mailimage.PublicURL(src):
				case strings.HasPrefix(strings.ToLower(src), "data:image/"):
					if _, _, err := mailimage.Data(src); err != nil {
						return
					}
				default:
					return
				}
				if !seen[img.ID] {
					images = append(images, img)
					seen[img.ID] = true
				}
				text.WriteString("\n" + img.token() + "\n")
				return
			}
		}
		if n.Type == html.TextNode {
			text.WriteString(n.Data)
		}
		for child := n.FirstChild; child != nil; child = child.NextSibling {
			walk(child, depth+1)
		}
		if n.Type == html.ElementNode && n.Data == "a" {
			for _, a := range n.Attr {
				if a.Key == "href" && (strings.HasPrefix(a.Val, "https://") || strings.HasPrefix(a.Val, "http://")) {
					text.WriteString(" (" + a.Val + ")")
				}
			}
		}
	}
	walk(root, 0)
	var lines []string
	for _, line := range strings.Split(safeMailText(text.String()), "\n") {
		if line = strings.Join(strings.Fields(line), " "); line != "" {
			lines = append(lines, line)
		}
	}
	return strings.Join(lines, "\n"), images
}

func indexMailImages(r *remoteMail, parts []messageImage, htmlSource string) {
	r.Images, r.ImageBody = nil, ""
	seen := map[string]bool{}
	if htmlSource != "" {
		body, images := htmlImages(htmlSource, parts)
		if len(images) > 0 {
			r.Images, r.ImageBody = images, body
			for _, img := range images {
				seen[img.ID] = true
			}
		}
	}
	for _, img := range parts {
		// A hidden/unreferenced inline CID image must not reappear as an
		// attachment preview. Explicit attachments remain available to load.
		if seen[img.ID] || len(r.Images) >= maxMessageImages || (htmlSource != "" && img.CID != "" && !img.Attachment) {
			continue
		}
		if r.ImageBody == "" {
			r.ImageBody = r.Body
		}
		r.Images = append(r.Images, img)
		r.ImageBody += "\n" + img.token()
	}
	if r.ImageBody != "" && htmlSource != "" && len(r.Attachments) > 0 {
		r.ImageBody += "\n\nAttachments: " + strings.Join(r.Attachments, ", ")
	}
	if len(r.ImageBody) > 2<<20 {
		r.ImageBody = safeMailText(r.ImageBody[:2<<20]) + "\n[Image view text truncated at 2 MiB.]"
	}
	visible := r.Images[:0]
	for _, img := range r.Images {
		if strings.Contains(r.ImageBody, img.token()) {
			visible = append(visible, img)
		}
	}
	r.Images = visible
	r.ImagesIndexed = true
}
