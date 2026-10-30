package main

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/smtp"
	"net/textproto"
	"sort"
	"strings"
	"time"

	"github.com/emersion/go-imap"
	"github.com/emersion/go-imap/client"
)

var errThreadLimit = errors.New("Thread exceeds 500 message IDs or 12 lookup rounds; some messages may be missing.")

type syncRequest struct {
	Folder    string
	Limit     int
	ThreadID  string
	ReadEpoch uint64
}
type syncResult struct {
	Data         accountMail
	Applied      []string
	TrashApplied []string
	TrashUpdates []trashChange
	TrashWarning string
	Err          error
}
type deliveryResult struct {
	Accepted, Uncertain bool
	Err                 error
}
type mailTransport interface {
	Sync(context.Context, AccountConfig, string, accountMail, syncRequest) syncResult
	Send(context.Context, AccountConfig, string, outgoingMail) deliveryResult
	FileSent(context.Context, AccountConfig, string, accountMail, outgoingMail) error
}
type imapTransport struct{ roots *x509.CertPool }

func (b imapTransport) connect(ctx context.Context, c AccountConfig, password string) (*client.Client, func(), error) {
	if _, s := c.validate(); s != "" {
		return nil, func() {}, errors.New(s)
	}
	conn, err := (&net.Dialer{}).DialContext(ctx, "tcp", net.JoinHostPort(c.IMAPHost, fmt.Sprint(c.IMAPPort)))
	if err != nil {
		return nil, func() {}, connectionError(ctx, "IMAP", err, false)
	}
	stop := context.AfterFunc(ctx, func() { conn.Close() })
	close := func() { stop(); conn.Close() }
	deadline, _ := ctx.Deadline()
	conn.SetDeadline(deadline)
	secured := tls.Client(conn, &tls.Config{ServerName: c.IMAPHost, MinVersion: tls.VersionTLS12, RootCAs: b.roots})
	if err = secured.HandshakeContext(ctx); err != nil {
		close()
		return nil, func() {}, connectionError(ctx, "IMAP TLS", err, false)
	}
	im, err := client.New(secured)
	if err != nil {
		close()
		return nil, func() {}, connectionError(ctx, "IMAP greeting", err, false)
	}
	im.ErrorLog = log.New(io.Discard, "", 0)
	if err = im.Login(c.IMAPUser, password); err != nil {
		close()
		return nil, func() {}, connectionError(ctx, "IMAP sign-in", err, true)
	}
	return im, func() { im.Terminate(); close() }, nil
}
func containsFlag(flags []string, s string) bool {
	for _, f := range flags {
		if strings.EqualFold(f, s) {
			return true
		}
	}
	return false
}
func listLiveFolders(im *client.Client, old []folderInfo) ([]folderInfo, error) {
	ch := make(chan *imap.MailboxInfo, 32)
	done := make(chan error, 1)
	go func() { done <- im.List("", "*", ch) }()
	var infos []*imap.MailboxInfo
	for box := range ch {
		if !containsFlag(box.Attributes, imap.NoSelectAttr) && safeField(box.Name) {
			infos = append(infos, box)
		}
	}
	if err := <-done; err != nil {
		return nil, err
	}
	var out []folderInfo
	for _, box := range infos {
		f := folderInfo{Name: box.Name}
		for _, o := range old {
			if o.Name == box.Name {
				f = o
			}
		}
		for _, role := range []string{imap.SentAttr, imap.DraftsAttr, imap.TrashAttr, imap.JunkAttr} {
			if containsFlag(box.Attributes, role) {
				f.Role = role
			}
		}
		if f.Role == "" {
			switch strings.ToLower(box.Name) {
			case "sent", "sent messages", "sent mail", "[gmail]/sent mail":
				f.Role = imap.SentAttr
			case "drafts", "draft messages", "[gmail]/drafts":
				f.Role = imap.DraftsAttr
			case "trash", "deleted messages", "deleted items", "[gmail]/trash", "[gmail]/bin":
				f.Role = imap.TrashAttr
			}
		}
		s, err := im.Status(box.Name, []imap.StatusItem{imap.StatusMessages, imap.StatusUnseen, imap.StatusUidValidity})
		if err != nil {
			return nil, err
		}
		f.Total, f.Unread, f.Validity = s.Messages, s.Unseen, s.UidValidity
		out = append(out, f)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if strings.EqualFold(out[i].Name, "INBOX") {
			return true
		}
		if strings.EqualFold(out[j].Name, "INBOX") {
			return false
		}
		return out[i].Name < out[j].Name
	})
	return out, nil
}
func uidSet(uids []uint32) *imap.SeqSet { s := new(imap.SeqSet); s.AddNum(uids...); return s }
func fetchLiveMail(im *client.Client, folder string, validity uint32, uids []uint32, old map[string]remoteMail) ([]remoteMail, error) {
	if len(uids) == 0 {
		return nil, nil
	}
	header := &imap.BodySectionName{BodyPartName: imap.BodyPartName{Specifier: imap.HeaderSpecifier}, Peek: true, Partial: []int{0, 64 << 10}}
	ch := make(chan *imap.Message, 32)
	done := make(chan error, 1)
	go func() {
		done <- im.UidFetch(uidSet(uids), []imap.FetchItem{imap.FetchUid, imap.FetchFlags, imap.FetchInternalDate, imap.FetchEnvelope, imap.FetchBodyStructure, header.FetchItem()}, ch)
	}()
	type fetched struct {
		r  remoteMail
		bs *imap.BodyStructure
	}
	var result []fetched
	for m := range ch {
		if m.Uid == 0 {
			continue
		}
		ref := mailRef{folder, validity, m.Uid}
		r := old[ref.key()]
		r.Ref = ref
		r.Seen = containsFlag(m.Flags, imap.SeenFlag)
		r.Starred = containsFlag(m.Flags, imap.FlaggedFlag)
		r.Received = m.InternalDate
		if m.Envelope != nil {
			r.Subject = safeMailText(m.Envelope.Subject)
			if len(m.Envelope.From) > 0 {
				r.From = m.Envelope.From[0].Address()
				r.Sender = safeMailText(m.Envelope.From[0].PersonalName)
			}
		}
		if lit := m.GetBody(header); lit != nil {
			data, _ := io.ReadAll(io.LimitReader(lit, 64<<10))
			applyHeader(&r, data)
		}
		if r.MessageID == "" {
			// Preserve cached synthetic IDs across the app rename.
			r.MessageID = fmt.Sprintf("<local-%d-%d-%x@sorakyaku.invalid>", validity, m.Uid, folder)
		}
		if r.Subject == "" {
			r.Subject = "(no subject)"
		}
		if r.Sender == "" {
			r.Sender = safeMailText(r.From)
		}
		result = append(result, fetched{r, m.BodyStructure})
	}
	if err := <-done; err != nil {
		return nil, err
	}
	var out []remoteMail
	for _, f := range result {
		r := f.r
		if !r.BodyLoaded {
			parts, attachments := readableParts(f.bs)
			tooManyParts := len(parts) > 16
			r.Attachments = attachments
			if len(parts) > 16 {
				parts = parts[:16]
			}
			partLimit := (1 << 20) / max(1, len(parts))
			var sections []*imap.BodySectionName
			items := []imap.FetchItem{imap.FetchUid}
			for _, p := range parts {
				s := &imap.BodySectionName{BodyPartName: imap.BodyPartName{Path: p.Path}, Peek: true, Partial: []int{0, partLimit}}
				sections = append(sections, s)
				items = append(items, s.FetchItem())
			}
			var body []string
			if len(parts) > 0 {
				bodyCh := make(chan *imap.Message, 1)
				bodyDone := make(chan error, 1)
				go func() { bodyDone <- im.UidFetch(uidSet([]uint32{r.Ref.UID}), items, bodyCh) }()
				found := false
				for m := range bodyCh {
					found = true
					for i, s := range sections {
						if lit := m.GetBody(s); lit != nil {
							data, _ := io.ReadAll(io.LimitReader(lit, int64(partLimit)))
							text := decodeTextPart(parts[i], data)
							if parts[i].Size > uint32(partLimit) {
								text += "\n[Text part truncated to keep this message within its 1 MiB download limit.]"
							}
							body = append(body, text)
						}
					}
				}
				if err := <-bodyDone; err != nil {
					return out, err
				}
				if !found {
					continue
				}
			}
			r.Body = strings.TrimSpace(strings.Join(body, "\n\n"))
			if len(r.Body) > 2<<20 {
				r.Body = safeMailText(r.Body[:2<<20]) + "\n[Decoded text truncated at 2 MiB.]"
			}
			if tooManyParts {
				r.Body += "\n[Additional text parts omitted after the first 16.]"
			}
			if r.Body == "" {
				r.Body = "[No readable text in this message.]"
			}
			if len(attachments) > 0 {
				r.Body += "\n\nAttachments: " + strings.Join(attachments, ", ") + "\n[Attachment downloads are not supported yet.]"
			}
			r.BodyLoaded = true
		}
		out = append(out, r)
	}
	return out, nil
}
func mergeRemote(a *accountMail, messages []remoteMail) {
	pos := map[string]int{}
	for i, r := range a.Messages {
		pos[r.Ref.key()] = i
	}
	for _, r := range messages {
		if i, ok := pos[r.Ref.key()]; ok {
			a.Messages[i] = r
		} else {
			pos[r.Ref.key()] = len(a.Messages)
			a.Messages = append(a.Messages, r)
		}
	}
}
func recordMap(a accountMail) map[string]remoteMail {
	out := map[string]remoteMail{}
	for _, r := range a.Messages {
		out[r.Ref.key()] = r
	}
	return out
}
func (b imapTransport) Sync(ctx context.Context, c AccountConfig, password string, a accountMail, req syncRequest) syncResult {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Minute)
	defer cancel()
	res := syncResult{Data: a, TrashUpdates: append([]trashChange{}, a.Trash...)}
	fail := func(stage string, err error) syncResult {
		res.Err = connectionError(ctx, stage, err, false)
		return res
	}
	im, close, err := b.connect(ctx, c, password)
	if err != nil {
		res.Err = err
		return res
	}
	defer close()
	folders, err := listLiveFolders(im, a.Folders)
	if err != nil {
		return fail("Folder sync", err)
	}
	res.Data.Folders = folders
	valid := map[string]uint32{}
	for _, f := range folders {
		valid[f.Name] = f.Validity
	}
	kept := res.Data.Messages[:0]
	for _, r := range res.Data.Messages {
		if valid[r.Ref.Folder] == r.Ref.Validity {
			kept = append(kept, r)
		}
	}
	res.Data.Messages = kept
	for _, op := range a.Flags {
		if valid[op.Ref.Folder] != op.Ref.Validity {
			res.Applied = append(res.Applied, op.ID)
			continue
		}
		box, err := im.Select(op.Ref.Folder, false)
		if err != nil {
			return fail("Read status sync", err)
		}
		if box.UidValidity != op.Ref.Validity {
			res.Applied = append(res.Applied, op.ID)
			continue
		}
		operation := imap.FlagsOp(imap.AddFlags)
		if !op.Value {
			operation = imap.RemoveFlags
		}
		if err = im.UidStore(uidSet([]uint32{op.Ref.UID}), imap.FormatFlagsOp(operation, true), []interface{}{op.Flag}, nil); err != nil {
			return fail("Read status sync", err)
		}
		res.Applied = append(res.Applied, op.ID)
	}
	for i, op := range a.Trash {
		updated, done, stale, err := moveToTrash(im, res.Data.Folders, op)
		res.TrashUpdates[i] = updated
		if err != nil {
			var problem *connectionProblem
			if errors.As(err, &problem) {
				res.Err = problem
				return res
			}
			return fail("Move to Trash", err)
		}
		if !done {
			continue
		}
		res.TrashApplied = append(res.TrashApplied, op.ID)
		if stale {
			res.TrashWarning = "A queued move was canceled because the source folder changed. Select the mail again to move it."
		} else {
			removeMovedCache(&res.Data, op.Refs)
		}
	}
	for _, folder := range res.Data.Folders {
		if folder.Name == req.Folder {
			continue
		}
		var known []uint32
		for _, r := range res.Data.Messages {
			if r.Ref.Folder == folder.Name {
				known = append(known, r.Ref.UID)
			}
		}
		if len(known) == 0 {
			continue
		}
		if _, err := im.Select(folder.Name, true); err != nil {
			return fail("Cached folder sync", err)
		}
		ch := make(chan *imap.Message, 32)
		done := make(chan error, 1)
		go func() { done <- im.UidFetch(uidSet(known), []imap.FetchItem{imap.FetchUid, imap.FetchFlags}, ch) }()
		flags := map[uint32][]string{}
		for r := range ch {
			flags[r.Uid] = r.Flags
		}
		if err := <-done; err != nil {
			return fail("Cached read status sync", err)
		}
		kept := res.Data.Messages[:0]
		for _, r := range res.Data.Messages {
			if r.Ref.Folder == folder.Name {
				f, exists := flags[r.Ref.UID]
				if !exists || containsFlag(f, imap.DeletedFlag) {
					continue
				}
				r.Seen = containsFlag(f, imap.SeenFlag)
				r.Starred = containsFlag(f, imap.FlaggedFlag)
			}
			kept = append(kept, r)
		}
		res.Data.Messages = kept
	}

	if req.Folder != "" && req.Folder != localDraftFolder {
		box, err := im.Select(req.Folder, true)
		if err != nil {
			return fail("Open folder", err)
		}
		search := imap.NewSearchCriteria()
		search.WithoutFlags = []string{imap.DeletedFlag}
		all, err := im.UidSearch(search)
		if err != nil {
			return fail("Message sync", err)
		}
		live := map[uint32]bool{}
		for _, id := range all {
			live[id] = true
		}
		kept := res.Data.Messages[:0]
		for _, r := range res.Data.Messages {
			if r.Ref.Folder != req.Folder || (r.Ref.Validity == box.UidValidity && live[r.Ref.UID]) {
				kept = append(kept, r)
			}
		}
		res.Data.Messages = kept
		// Fetch flags for every cached message, including previously loaded pages.
		var known []uint32
		for _, r := range res.Data.Messages {
			if r.Ref.Folder == req.Folder {
				known = append(known, r.Ref.UID)
			}
		}
		if len(known) > 0 {
			ch := make(chan *imap.Message, 32)
			done := make(chan error, 1)
			go func() { done <- im.UidFetch(uidSet(known), []imap.FetchItem{imap.FetchUid, imap.FetchFlags}, ch) }()
			flags := map[uint32][]string{}
			for m := range ch {
				flags[m.Uid] = m.Flags
			}
			if err := <-done; err != nil {
				return fail("Read status sync", err)
			}
			for i := range res.Data.Messages {
				r := &res.Data.Messages[i]
				if r.Ref.Folder == req.Folder {
					if f, ok := flags[r.Ref.UID]; ok {
						r.Seen = containsFlag(f, imap.SeenFlag)
						r.Starred = containsFlag(f, imap.FlaggedFlag)
					}
				}
			}
		}
		sort.Slice(all, func(i, j int) bool { return all[i] > all[j] })
		limit := req.Limit
		if limit < 50 {
			limit = 50
		}
		if len(all) > limit {
			all = all[:limit]
		}
		old := recordMap(res.Data)
		var need []uint32
		for _, id := range all {
			if r, ok := old[(mailRef{req.Folder, box.UidValidity, id}).key()]; !ok || !r.BodyLoaded {
				need = append(need, id)
			}
		}
		for start := 0; start < len(need); start += 25 {
			records, err := fetchLiveMail(im, req.Folder, box.UidValidity, need[start:min(start+25, len(need))], old)
			mergeRemote(&res.Data, records)
			if err != nil {
				return fail("Message download", err)
			}
		}
	}
	if req.ThreadID != "" {
		if err := expandRemoteThread(im, &res.Data, req.ThreadID); err != nil {
			if errors.Is(err, errThreadLimit) {
				res.Err = err
				return res
			}
			return fail("Thread sync", err)
		}
	}
	// Re-read counters after STORE; counts are server message counts, not page counts.
	for i := range res.Data.Folders {
		f := &res.Data.Folders[i]
		s, err := im.Status(f.Name, []imap.StatusItem{imap.StatusMessages, imap.StatusUnseen})
		if err != nil {
			return fail("Folder counts", err)
		}
		f.Total, f.Unread = s.Messages, s.Unseen
		f.Loaded = 0
		for _, r := range res.Data.Messages {
			if r.Ref.Folder == f.Name {
				f.Loaded++
			}
		}
		f.Complete = uint32(f.Loaded) >= f.Total
	}
	res.Data.Synced = time.Now()
	return res
}
func searchIDs(ids []string) *imap.SearchCriteria {
	var clauses []*imap.SearchCriteria
	for _, id := range ids {
		for _, field := range []string{"Message-ID", "References", "In-Reply-To"} {
			c := imap.NewSearchCriteria()
			c.Header = textproto.MIMEHeader{field: []string{id}}
			clauses = append(clauses, c)
		}
	}
	result := clauses[0]
	for _, c := range clauses[1:] {
		n := imap.NewSearchCriteria()
		n.Or = [][2]*imap.SearchCriteria{{result, c}}
		result = n
	}
	result.WithoutFlags = []string{imap.DeletedFlag}
	return result
}
func expandRemoteThread(im *client.Client, a *accountMail, seed string) error {
	frontier := []string{seed}
	for _, r := range a.Messages {
		if r.MessageID == seed {
			if len(r.References) > 0 {
				frontier = append(frontier, r.References...)
			}
			if r.InReplyTo != "" {
				frontier = append(frontier, r.InReplyTo)
			}
		}
	}
	searched := map[string]bool{}
	for round := 0; round < 12; round++ {
		var batch []string
		for _, id := range frontier {
			if !searched[id] {
				searched[id] = true
				batch = append(batch, id)
			}
		}
		if len(batch) == 0 {
			return nil
		}
		if len(searched) > 500 {
			return errThreadLimit
		}
		frontier = nil
		for _, folder := range a.Folders {
			box, err := im.Select(folder.Name, true)
			if err != nil {
				return err
			}
			for start := 0; start < len(batch); start += 12 {
				found, err := im.UidSearch(searchIDs(batch[start:min(start+12, len(batch))]))
				if err != nil {
					return err
				}
				if len(found) > 500 {
					return errThreadLimit
				}
				old := recordMap(*a)
				var need []uint32
				for _, id := range found {
					ref := mailRef{folder.Name, box.UidValidity, id}
					if r, ok := old[ref.key()]; !ok || !r.BodyLoaded {
						need = append(need, id)
					} else {
						frontier = append(frontier, r.MessageID)
						frontier = append(frontier, r.References...)
						if r.InReplyTo != "" {
							frontier = append(frontier, r.InReplyTo)
						}
					}
				}
				records, err := fetchLiveMail(im, folder.Name, box.UidValidity, need, old)
				if err != nil {
					return err
				}
				mergeRemote(a, records)
				for _, r := range records {
					frontier = append(frontier, r.MessageID)
					frontier = append(frontier, r.References...)
					if r.InReplyTo != "" {
						frontier = append(frontier, r.InReplyTo)
					}
				}
			}
		}
	}
	return errThreadLimit
}
func (b imapTransport) Send(ctx context.Context, c AccountConfig, password string, o outgoingMail) deliveryResult {
	ctx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	if o.State == "retry" {
		im, close, err := b.connect(ctx, c, password)
		if err != nil {
			return deliveryResult{Uncertain: true, Err: err}
		}
		folders, err := listLiveFolders(im, nil)
		if err != nil {
			close()
			return deliveryResult{Uncertain: true, Err: connectionError(ctx, "Check previous delivery", err, false)}
		}
		for _, f := range folders {
			if f.Role == imap.SentAttr {
				if _, err = im.Select(f.Name, true); err != nil {
					close()
					return deliveryResult{Uncertain: true, Err: connectionError(ctx, "Check Sent", err, false)}
				}
				criteria := imap.NewSearchCriteria()
				criteria.Header = textproto.MIMEHeader{"Message-ID": []string{o.ID}}
				found, err := im.UidSearch(criteria)
				if err != nil {
					close()
					return deliveryResult{Uncertain: true, Err: connectionError(ctx, "Check Sent", err, false)}
				}
				if len(found) > 0 {
					close()
					return deliveryResult{Accepted: true}
				}
				break
			}
		}
		close()
	}
	_, targets, err := makeWire(c, o)
	if err != nil {
		return deliveryResult{Err: err}
	}
	fail := func(stage string, err error) deliveryResult {
		return deliveryResult{Err: connectionError(ctx, stage, err, false)}
	}
	conn, err := (&net.Dialer{}).DialContext(ctx, "tcp", net.JoinHostPort(c.SMTPHost, fmt.Sprint(c.SMTPPort)))
	if err != nil {
		return fail("SMTP", err)
	}
	defer conn.Close()
	stop := context.AfterFunc(ctx, func() { conn.Close() })
	defer stop()
	deadline, _ := ctx.Deadline()
	conn.SetDeadline(deadline)
	cfg := &tls.Config{ServerName: c.SMTPHost, MinVersion: tls.VersionTLS12, RootCAs: b.roots}
	var transport net.Conn = conn
	if c.SMTPSecurity == "TLS" {
		t := tls.Client(conn, cfg)
		if err = t.HandshakeContext(ctx); err != nil {
			return fail("SMTP TLS", err)
		}
		transport = t
	}
	s, err := smtp.NewClient(transport, c.SMTPHost)
	if err != nil {
		return fail("SMTP", err)
	}
	defer s.Close()
	if c.SMTPSecurity == "STARTTLS" {
		if ok, _ := s.Extension("STARTTLS"); !ok {
			return deliveryResult{Err: errors.New("SMTP requires STARTTLS; server did not offer it.")}
		}
		if err = s.StartTLS(cfg); err != nil {
			return fail("SMTP TLS", err)
		}
	}
	if err = s.Auth(smtp.PlainAuth("", c.SMTPUser, password, c.SMTPHost)); err != nil {
		return deliveryResult{Err: connectionError(ctx, "SMTP sign-in", err, true)}
	}
	if err = s.Mail(c.Email); err != nil {
		return fail("SMTP sender", err)
	}
	for _, to := range targets {
		if err = s.Rcpt(to); err != nil {
			return deliveryResult{Err: errors.New("A recipient was rejected. No message was submitted; check all recipients.")}
		}
	}
	w, err := s.Data()
	if err != nil {
		return fail("SMTP submission", err)
	}
	if _, err = w.Write(o.Wire); err != nil {
		return deliveryResult{Uncertain: true, Err: errors.New("Connection lost during submission. Delivery is uncertain; check Sent before retrying.")}
	}
	if err = w.Close(); err != nil {
		var response *textproto.Error
		if errors.As(err, &response) {
			return deliveryResult{Err: errors.New("SMTP rejected the message. Draft retained; check message size and try again later.")}
		}
		return deliveryResult{Uncertain: true, Err: errors.New("Server acceptance could not be confirmed. Check Sent before using :retry-send.")}
	}
	return deliveryResult{Accepted: true}
}
func (b imapTransport) FileSent(ctx context.Context, c AccountConfig, password string, a accountMail, o outgoingMail) error {
	ctx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	im, close, err := b.connect(ctx, c, password)
	if err != nil {
		return err
	}
	defer close()
	folder := ""
	for _, f := range a.Folders {
		if f.Role == imap.SentAttr {
			folder = f.Name
			break
		}
	}
	if folder == "" {
		return errors.New("Sent folder was not discovered. The message was delivered; configure a Sent folder before saving its copy.")
	}
	if _, err = im.Select(folder, true); err != nil {
		return connectionError(ctx, "Sent folder", err, false)
	}
	criteria := imap.NewSearchCriteria()
	criteria.Header = textproto.MIMEHeader{"Message-ID": []string{o.ID}}
	found, err := im.UidSearch(criteria)
	if err != nil {
		return connectionError(ctx, "Sent copy check", err, false)
	}
	if len(found) > 0 {
		return nil
	}
	if err = im.Append(folder, []string{imap.SeenFlag}, o.Date, bytes.NewReader(o.Wire)); err != nil {
		return connectionError(ctx, "Sent copy", err, false)
	}
	return nil
}
