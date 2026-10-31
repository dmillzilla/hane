package app

import (
	"strings"
	"testing"

	"github.com/emersion/go-imap"
)

func TestHTMLImagePositionsCIDAndTrackingFilters(t *testing.T) {
	part := messageImage{ID: imageID("cid-part"), CID: "postcard@example.com", Name: "postcard.png", Part: []int{2}, MIMEType: "image/png"}
	source := `<p>Before photo</p><img src="cid:postcard%40example.com" alt="Postcard"><p>After photo</p>
<img src="https://images.example/photo.png" alt="Remote photo">
<img src="https://images.example/pixel" width="1" height="1">
<div style="display: none"><img src="https://images.example/hidden"></div>
<img src="https://images.example/transparent" style="opacity: 0.0">
<img src="https://images.example/css-pixel" style="width: 1px">
<img src="http://127.0.0.1/private"><img src="file:///etc/passwd">
<script><img src="https://images.example/script"></script>`
	body, images := htmlImages(source, []messageImage{part})
	if len(images) != 2 || images[0].Source != "" || len(images[0].Part) != 1 || images[1].Source != "https://images.example/photo.png" {
		t.Fatalf("unexpected image metadata: %+v", images)
	}
	before, photo, after := strings.Index(body, "Before photo"), strings.Index(body, images[0].token()), strings.Index(body, "After photo")
	if before < 0 || photo <= before || after <= photo {
		t.Fatalf("image position was lost: %q", body)
	}
	for _, forbidden := range []string{"pixel", "hidden", "transparent", "private", "passwd", "script"} {
		if strings.Contains(body, forbidden) {
			t.Fatalf("hidden/unsafe content retained: %q", body)
		}
	}
}

func TestImageMIMEStructureAndAttachmentFallback(t *testing.T) {
	bs := &imap.BodyStructure{MIMEType: "multipart", MIMESubType: "related", Parts: []*imap.BodyStructure{
		{MIMEType: "multipart", MIMESubType: "alternative", Parts: []*imap.BodyStructure{
			{MIMEType: "text", MIMESubType: "plain"},
			{MIMEType: "text", MIMESubType: "html", Encoding: "quoted-printable"},
		}},
		{MIMEType: "image", MIMESubType: "png", Id: "<inline@example.com>", Encoding: "base64"},
		{MIMEType: "image", MIMESubType: "jpeg", Disposition: "attachment", DispositionParams: map[string]string{"filename": "photo.jpg"}},
		{MIMEType: "image", MIMESubType: "svg+xml", Disposition: "attachment"},
	}}
	parts, html := imageMIMEParts(bs)
	if html == nil || len(html.Path) != 2 || html.Path[0] != 1 || html.Path[1] != 2 || len(parts) != 2 {
		t.Fatalf("MIME indexing failed: %+v, %+v", parts, html)
	}
	r := remoteMail{Body: "Plain body", Attachments: []string{"photo.jpg"}}
	indexMailImages(&r, parts, `<p>HTML body</p><img src="cid:inline@example.com" style="display:none">`)
	if !r.ImagesIndexed || len(r.Images) != 1 || r.Images[0].Name != "photo.jpg" || !strings.Contains(r.ImageBody, "Plain body") || !strings.Contains(r.ImageBody, r.Images[0].token()) {
		t.Fatalf("hidden CID reappeared or attachment was lost: %+v", r)
	}
}

func TestImageLabelsAreSanitizedAndSourcesDeduplicated(t *testing.T) {
	body, images := htmlImages(`<img src="//images.example/photo.png" alt="hello&#27;[31m world"><img src="https://images.example/photo.png">`, nil)
	if len(images) != 1 || strings.ContainsRune(images[0].Name, '\x1b') || images[0].Source != "https://images.example/photo.png" || strings.Count(body, images[0].token()) != 2 {
		t.Fatal("source deduplication or label sanitization failed")
	}
	var source strings.Builder
	for i := 0; i < 100; i++ {
		source.WriteString(`<img src="https://images.example/` + strings.Repeat("a", i+1) + `">`)
	}
	_, images = htmlImages(source.String(), nil)
	if len(images) > maxMessageImages {
		t.Fatal("HTML image count was not bounded")
	}
}

func TestAttachedMultipartDoesNotReplaceMainHTMLBody(t *testing.T) {
	bs := &imap.BodyStructure{MIMEType: "multipart", MIMESubType: "mixed", Disposition: "attachment", Parts: []*imap.BodyStructure{
		{MIMEType: "text", MIMESubType: "html"},
		{MIMEType: "image", MIMESubType: "png"},
	}}
	images, html := imageMIMEParts(bs)
	if len(images) != 0 || html != nil {
		t.Fatal("an attached multipart document was treated as the main mail body")
	}
}
