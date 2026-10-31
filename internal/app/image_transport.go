package app

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"

	"github.com/emersion/go-imap"
	message "github.com/emersion/go-message"
)

// Image fetches just one selected MIME part, using BODY.PEEK in a read-only
// mailbox. The source UIDVALIDITY is checked before touching any message UID.
func (b imapTransport) Image(ctx context.Context, c AccountConfig, password string, ref mailRef, part messageImage, limit int64) ([]byte, string, error) {
	if len(part.Part) == 0 || len(part.Part) > 24 || !supportedImage(part.MIMEType) || ref.UID == 0 || ref.Validity == 0 || limit <= 0 {
		return nil, "", errors.New("invalid image attachment reference")
	}
	for _, number := range part.Part {
		if number < 1 {
			return nil, "", errors.New("invalid image MIME path")
		}
	}
	encodedLimit := limit*4/3 + 4096
	if int64(part.Size) > encodedLimit {
		return nil, "", errors.New("image attachment exceeds the download limit")
	}
	im, close, err := b.connect(ctx, c, password)
	if err != nil {
		return nil, "", err
	}
	defer close()
	box, err := im.Select(ref.Folder, true)
	if err != nil {
		return nil, "", errors.New("could not open the image's mailbox")
	}
	if box.UidValidity != ref.Validity {
		return nil, "", errors.New("mailbox changed; sync before loading this image")
	}
	section := &imap.BodySectionName{BodyPartName: imap.BodyPartName{Path: part.Part}, Peek: true, Partial: []int{0, int(encodedLimit + 1)}}
	ch := make(chan *imap.Message, 1)
	done := make(chan error, 1)
	go func() {
		done <- im.UidFetch(uidSet([]uint32{ref.UID}), []imap.FetchItem{imap.FetchUid, section.FetchItem()}, ch)
	}()
	var encoded []byte
	var readErr error
	for msg := range ch {
		if msg.Uid != ref.UID {
			continue
		}
		if literal := msg.GetBody(section); literal != nil {
			encoded, readErr = io.ReadAll(io.LimitReader(literal, encodedLimit+1))
		}
	}
	if err := <-done; err != nil || readErr != nil || len(encoded) == 0 {
		return nil, "", errors.New("could not download the image attachment")
	}
	if int64(len(encoded)) > encodedLimit {
		return nil, "", errors.New("image attachment exceeds the download limit")
	}
	var header message.Header
	header.Set("Content-Type", part.MIMEType)
	header.Set("Content-Transfer-Encoding", strings.ToLower(part.Encoding))
	entity, err := message.New(header, bytes.NewReader(encoded))
	if err != nil || entity == nil {
		return nil, "", errors.New("unsupported image attachment encoding")
	}
	data, err := io.ReadAll(io.LimitReader(entity.Body, limit+1))
	if err != nil || int64(len(data)) > limit {
		return nil, "", errors.New("image attachment is incomplete or too large")
	}
	return data, part.MIMEType, nil
}
