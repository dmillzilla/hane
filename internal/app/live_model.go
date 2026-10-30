package app

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/emersion/go-imap"
)

type liveState struct {
	path      string
	disk      mailDisk
	backend   mailTransport
	busy      map[string]bool
	queued    map[string]syncRequest
	limits    map[string]int
	passwords map[string]string
	expanded  map[string]bool
	pending   []tea.Cmd
	autosave  uint64
	sending   bool
	readEpoch uint64
	offline   bool
}
type liveSyncMsg struct {
	Key       string
	Request   syncRequest
	Result    syncResult
	Copies    []string
	CopyError string
}
type liveSendMsg struct {
	Key, ID string
	Result  deliveryResult
}
type mailRefreshMsg struct{}
type draftAutosaveMsg uint64

func refreshMailTick() tea.Cmd {
	return tea.Tick(60*time.Second, func(time.Time) tea.Msg { return mailRefreshMsg{} })
}
func (m *model) initLive(path string) error {
	disk, err := loadMailDisk(path)
	if err != nil {
		return fmt.Errorf("Cannot load mail storage at %s. The file has been preserved: %w", path, err)
	}
	m.live = &liveState{path: path, disk: disk, backend: imapTransport{}, busy: map[string]bool{}, queued: map[string]syncRequest{}, limits: map[string]int{}, passwords: map[string]string{}, expanded: map[string]bool{}}
	var accounts []Account
	for _, a := range m.accounts {
		if a.Config != nil {
			accounts = append(accounts, a)
		}
	}
	if len(accounts) > 0 {
		m.accounts = accounts
		m.threads = nil
		m.expanded = make([]bool, len(accounts))
		for i := range m.expanded {
			m.expanded[i] = true
		}
		m.account = 0
		m.folder = "INBOX"
		m.selected = 0
		m.treeCursor = 1
	}
	m.rebuildLive()
	if len(accounts) > 0 {
		m.folder = inboxName(m.accounts[0])
		m.notice = "Connecting · cached mail and drafts are available immediately."
	}
	return nil
}
func inboxName(a Account) string {
	for _, f := range a.Folders {
		if strings.EqualFold(f, "INBOX") {
			return f
		}
	}
	for _, f := range a.Folders {
		if f != localDraftFolder && f != syncQueueFolder {
			return f
		}
	}
	return localDraftFolder
}
func (m model) liveKey(index int) string {
	if index < 0 || index >= len(m.accounts) || m.accounts[index].Config == nil {
		return ""
	}
	return credentialKey(*m.accounts[index].Config)
}
func (m model) realAccount(index int) bool { return m.live != nil && m.liveKey(index) != "" }
func (m *model) commitMail(key string, a accountMail) bool {
	old, exists := m.live.disk.Accounts[key]
	m.live.disk.Accounts[key] = a
	if err := saveMailDisk(m.live.path, m.live.disk); err != nil {
		if exists {
			m.live.disk.Accounts[key] = old
		} else {
			delete(m.live.disk.Accounts, key)
		}
		m.notice = "Mail storage failed. Changes were not saved; check free space and permissions."
		return false
	}
	return true
}
func (m *model) queueSync(index int, req syncRequest) {
	if m.live != nil && m.live.offline {
		return
	}
	key := m.liveKey(index)
	if key == "" || m.live == nil {
		return
	}
	if req.Folder == "" {
		req.Folder = inboxName(m.accounts[index])
	}
	if req.Folder == syncQueueFolder {
		req.Folder = inboxName(m.accounts[index])
		for _, op := range m.live.disk.Accounts[key].Trash {
			if op.State != "uncertain" && len(op.Refs) > 0 {
				req.Folder = op.Refs[0].Folder
				break
			}
		}
	}
	limitKey := key + "/" + req.Folder
	if req.Limit == 0 {
		loaded := 0
		for _, f := range m.live.disk.Accounts[key].Folders {
			if f.Name == req.Folder {
				loaded = f.Loaded
			}
		}
		req.Limit = max(50, loaded, m.live.limits[limitKey])
	}
	m.live.limits[limitKey] = req.Limit
	if m.live.busy[key] {
		m.live.queued[key] = req
		return
	}
	c := *m.accounts[index].Config
	a := copyAccount(m.live.disk.Accounts[key])
	// Record that COPY may run before starting the worker. The worker gets the
	// ready snapshot; a crash leaves a conservative, non-retrying receipt.
	staged := copyAccount(a)
	dirty := false
	for i := range staged.Trash {
		if staged.Trash[i].State == "ready" {
			staged.Trash[i].State = "copying"
			dirty = true
		}
	}
	if dirty && !m.commitMail(key, staged) {
		return
	}
	backend := m.live.backend
	store := m.credentials
	sessionPassword := m.live.passwords[key]
	parent := m.background
	if parent == nil {
		parent = context.Background()
	}
	m.live.busy[key] = true
	m.accounts[index].ConnectionStatus = "Syncing…"
	m.live.pending = append(m.live.pending, func() tea.Msg {
		ctx, cancel := context.WithTimeout(parent, 5*time.Minute)
		defer cancel()
		p := sessionPassword
		var err error
		if p == "" {
			if !c.RememberPassword || store == nil {
				err = errors.New("Password needed · B password setup or e session sign-in")
			} else {
				p, err = store.Get(ctx, c)
			}
		}
		if err != nil {
			return liveSyncMsg{Key: key, Request: req, Result: syncResult{Data: a, TrashUpdates: a.Trash, Err: err}}
		}
		result := backend.Sync(ctx, c, p, a, req)
		out := liveSyncMsg{Key: key, Request: req, Result: result}
		if result.Err == nil {
			for _, o := range a.Outgoing {
				if o.State == "accepted" {
					if err := backend.FileSent(ctx, c, p, result.Data, o); err != nil {
						out.CopyError = err.Error()
					} else {
						out.Copies = append(out.Copies, o.ID)
					}
				}
			}
		}
		return out
	})
}
func (m *model) syncCurrent(more bool) {
	if more && m.folder == syncQueueFolder {
		m.notice = "The queue shows every pending move · R retries synchronization"
		return
	}
	if m.live != nil && m.live.offline {
		m.notice = "Offline mode · restart without --offline to sync."
		return
	}
	if !m.realAccount(m.account) {
		m.notice = "Select a configured account to sync."
		return
	}
	key := m.liveKey(m.account)
	limit := max(50, m.live.limits[key+"/"+m.folder])
	if more {
		limit += 100
	}
	m.queueSync(m.account, syncRequest{Folder: m.folder, Limit: limit})
	m.notice = "Syncing mail…"
}
func (m *model) expandThread(t *Thread) {
	if m.live != nil && m.live.offline {
		m.notice = "Offline · viewing cached conversation"
		return
	}
	if !t.Remote || len(t.RemoteIDs) == 0 {
		return
	}
	key := m.liveKey(t.Account) + "/" + t.RemoteIDs[0]
	if m.live.expanded[key] {
		return
	}
	m.live.expanded[key] = true
	m.queueSync(t.Account, syncRequest{Folder: t.Folder, ThreadID: t.RemoteIDs[0], ReadEpoch: m.live.readEpoch})
	m.notice = "Loading the conversation across account folders…"
}
func (m *model) finishLiveSync(msg liveSyncMsg) {
	delete(m.live.busy, msg.Key)
	index := -1
	for i := range m.accounts {
		if m.liveKey(i) == msg.Key {
			index = i
			break
		}
	}
	if index < 0 {
		return
	}
	current := copyAccount(m.live.disk.Accounts[msg.Key])
	data := msg.Result.Data
	data.Drafts = current.Drafts
	data.Outgoing = current.Outgoing
	trashAck := map[string]bool{}
	for _, id := range msg.Result.TrashApplied {
		trashAck[id] = true
	}
	data.Trash = nil
	for _, op := range current.Trash {
		if !trashAck[op.ID] {
			for _, updated := range msg.Result.TrashUpdates {
				if updated.ID == op.ID {
					op = updated
					op.Error = ""
					if msg.Result.Err != nil {
						op.Error = safeMailText(msg.Result.Err.Error())
					}
					break
				}
			}
			data.Trash = append(data.Trash, op)
		}
	}
	ack := map[string]bool{}
	for _, id := range msg.Result.Applied {
		ack[id] = true
	}
	present := map[string]bool{}
	for _, r := range data.Messages {
		present[r.MessageID] = true
	}
	// Retire a local Sent placeholder when its real message was moved, even
	// if this sync removed that record before normal placeholder retirement.
	for _, op := range current.Trash {
		if trashAck[op.ID] {
			for _, r := range current.Messages {
				if refsOverlap([]mailRef{r.Ref}, op.Refs) {
					present[r.MessageID] = true
				}
			}
		}
	}
	for i := range data.Outgoing {
		if present[data.Outgoing[i].ID] && (data.Outgoing[i].State == "sent" || data.Outgoing[i].State == "accepted") {
			data.Outgoing[i].State = "filed"
			data.Outgoing[i].Wire = nil
		}
	}
	data.Flags = nil
	for _, op := range current.Flags {
		if !ack[op.ID] {
			data.Flags = append(data.Flags, op)
		}
	}
	for _, id := range msg.Copies {
		for i := range data.Outgoing {
			if data.Outgoing[i].ID == id {
				if present[id] {
					data.Outgoing[i].State = "filed"
				} else {
					data.Outgoing[i].State = "sent"
				}
				data.Outgoing[i].Wire = nil
				data.Outgoing[i].Error = ""
			}
		}
	}
	applyPendingFlags(&data)
	if !m.commitMail(msg.Key, data) {
		return
	}
	a := &m.accounts[index]
	if msg.Result.Err != nil {
		a.ConnectionStatus = msg.Result.Err.Error()
		m.notice = "Sync incomplete · " + a.ConnectionStatus
		if msg.Request.ThreadID != "" {
			delete(m.live.expanded, msg.Key+"/"+msg.Request.ThreadID)
		}
	} else {
		a.Verified = true
		a.ConnectionStatus = "Synced " + time.Now().Format("15:04")
		m.notice = "Mail synchronized · R refresh · L older mail"
	}
	if msg.CopyError != "" {
		m.notice = "Message sent; Sent copy pending. R retries the copy only. " + msg.CopyError
	}
	if len(msg.Result.TrashApplied) > 0 && msg.Result.Err == nil {
		m.notice = "Moved to Trash · saved replies kept in Drafts (local)"
	}
	if msg.Result.TrashWarning != "" {
		m.notice = msg.Result.TrashWarning
	}
	if len(data.Trash) > 0 {
		m.notice += " · Q shows pending Trash moves"
		for _, op := range data.Trash {
			if op.State == "uncertain" {
				m.notice = "Trash copy needs review · Q opens the queue for details"
			}
		}
	}
	// An open message may disappear while its confirmed move finishes.
	if t := m.current(); t != nil && t.Account == index && m.reading {
		for _, op := range current.Trash {
			if trashAck[op.ID] && refsOverlap(t.Refs, op.Refs) {
				m.reading, m.scroll, m.focus = false, 0, listPane
			}
		}
	}
	m.rebuildLive()
	if msg.Result.Err == nil && msg.Request.ThreadID != "" && msg.Request.ReadEpoch == m.live.readEpoch {
		t := m.current()
		if m.compose != nil {
			t = m.replyThread()
		}
		if t != nil && t.Account == index && (m.reading || m.compose != nil) && stringIn(t.RemoteIDs, msg.Request.ThreadID) {
			m.markLiveThread(t, imap.SeenFlag, true)
		}
	}
	if req, ok := m.live.queued[msg.Key]; ok {
		delete(m.live.queued, msg.Key)
		m.queueSync(index, req)
	} else if (len(data.Flags) > 0 || trashWorkPending(data.Trash)) && msg.Result.Err == nil {
		m.queueSync(index, syncRequest{Folder: m.folder})
	}
}
func applyPendingFlags(a *accountMail) {
	for _, op := range a.Flags {
		for i := range a.Messages {
			r := &a.Messages[i]
			if r.Ref == op.Ref {
				if op.Flag == imap.SeenFlag {
					if r.Seen != op.Value {
						for i := range a.Folders {
							f := &a.Folders[i]
							if f.Name == r.Ref.Folder {
								if op.Value && f.Unread > 0 {
									f.Unread--
								} else if !op.Value {
									f.Unread++
								}
							}
						}
					}
					r.Seen = op.Value
				} else if op.Flag == imap.FlaggedFlag {
					r.Starred = op.Value
				}
			}
		}
	}
}
func (m *model) markLiveThread(t *Thread, flag string, value bool) bool {
	key := m.liveKey(t.Account)
	a := copyAccount(m.live.disk.Accounts[key])
	refs := map[mailRef]bool{}
	for _, r := range t.Refs {
		refs[r] = true
	}
	changed := false
	for i := range a.Messages {
		r := &a.Messages[i]
		if !refs[r.Ref] || r.Ref.UID == 0 {
			continue
		}
		old := r.Starred
		if flag == imap.SeenFlag {
			old = r.Seen
		}
		if old == value {
			continue
		}
		changed = true
		kept := a.Flags[:0]
		for _, op := range a.Flags {
			if op.Ref != r.Ref || op.Flag != flag {
				kept = append(kept, op)
			}
		}
		a.Flags = kept
		a.Flags = append(a.Flags, flagChange{opaqueID(), r.Ref, flag, value})
		if flag == imap.SeenFlag {
			r.Seen = value
			for j := range a.Folders {
				f := &a.Folders[j]
				if f.Name == r.Ref.Folder {
					if value && f.Unread > 0 {
						f.Unread--
					} else if !value {
						f.Unread++
					}
				}
			}
		} else {
			r.Starred = value
		}
	}
	if !changed {
		return true
	}
	if !m.commitMail(key, a) {
		return false
	}
	m.rebuildLive()
	m.queueSync(t.Account, syncRequest{Folder: m.folder})
	return true
}
func (m *model) rebuildLive() {
	if m.live == nil {
		return
	}
	selectedID := 0
	selectedMessage := ""
	if t := m.current(); t != nil {
		selectedID = t.ID
		if len(t.RemoteIDs) > 0 {
			selectedMessage = t.RemoteIDs[0]
		}
	}
	old := m.threads
	var out []Thread
	for _, t := range old {
		if !m.realAccount(t.Account) {
			out = append(out, t)
		}
	}
	for index := range m.accounts {
		key := m.liveKey(index)
		if key == "" {
			continue
		}
		a := m.live.disk.Accounts[key]
		if len(a.Folders) > 0 {
			m.accounts[index].Folders = nil
			for _, f := range a.Folders {
				m.accounts[index].Folders = append(m.accounts[index].Folders, f.Name)
			}
		}
		if !stringIn(m.accounts[index].Folders, localDraftFolder) {
			m.accounts[index].Folders = append(m.accounts[index].Folders, localDraftFolder)
		}
		if !stringIn(m.accounts[index].Folders, syncQueueFolder) {
			m.accounts[index].Folders = append(m.accounts[index].Folders, syncQueueFolder)
		}
		// Keep source records for recovery, but hide exact pending UIDs from
		// mail views. Never persist filtered data back over the cache.
		hidden := pendingTrashRefs(a)
		var records []remoteMail
		present := map[string]bool{}
		for _, r := range a.Messages {
			present[r.MessageID] = true
			if !hidden[r.Ref] {
				records = append(records, r)
			}
		}
		sentFolder := "Sent"
		for _, f := range a.Folders {
			if f.Role == imap.SentAttr {
				sentFolder = f.Name
			}
		}
		for _, o := range a.Outgoing {
			if (o.State == "accepted" || o.State == "sent") && !present[o.ID] {
				records = append(records, remoteMail{Ref: mailRef{Folder: sentFolder}, MessageID: o.ID, InReplyTo: o.ReplyID, References: o.References, Subject: o.Subject, Sender: "You", From: m.accounts[index].Address, To: o.To, Body: o.Body, Received: o.Date, Seen: true, BodyLoaded: true})
			}
		}
		threads := buildMailThreads(index, key, records, old)
		threads = append(threads, m.trashQueueThreads(index, a)...)
		for _, d := range a.Drafts {
			parent := 0
			for _, t := range threads {
				if stringIn(t.RemoteIDs, d.ReplyID) {
					parent = t.ID
					if t.Folder == m.folder {
						break
					}
				}
			}
			threads = append(threads, Thread{ID: d.ID, Account: index, Folder: localDraftFolder, Subject: d.Subject, LocalDraft: true, Remote: true, ReplyTo: parent, ReplyID: d.ReplyID, References: d.References, Recipient: d.To, Messages: []Message{{"You", m.accounts[index].Address, d.Body, d.Updated}}})
		}
		out = append(out, threads...)
	}
	m.threads = out
	normalize(m.threads)
	for i, t := range m.folderThreads() {
		if t.ID == selectedID || (selectedMessage != "" && stringIn(t.RemoteIDs, selectedMessage)) {
			m.selected = i
			break
		}
	}
	if m.compose != nil && m.realAccount(m.compose.account) && m.compose.replyID != "" {
		for _, t := range m.threads {
			if t.Account == m.compose.account && !isDraftThread(&t) && stringIn(t.RemoteIDs, m.compose.replyID) {
				m.compose.threadID = t.ID
				m.editorReturnID = t.ID
				if t.Folder == m.folder {
					break
				}
			}
		}
	}
	m.normalizeSelection()
}
func stringIn(ss []string, s string) bool {
	for _, v := range ss {
		if v == s {
			return true
		}
	}
	return false
}
func buildMailThreads(index int, key string, records []remoteMail, old []Thread) []Thread {
	parent := map[string]string{}
	var root func(string) string
	root = func(s string) string {
		p, ok := parent[s]
		if !ok {
			parent[s] = s
			return s
		}
		if p != s {
			parent[s] = root(p)
		}
		return parent[s]
	}
	join := func(a, b string) {
		ra, rb := root(a), root(b)
		if ra < rb {
			parent[rb] = ra
		} else {
			parent[ra] = rb
		}
	}
	for _, r := range records {
		root(r.MessageID)
		for _, id := range r.References {
			join(r.MessageID, id)
		}
		if r.InReplyTo != "" {
			join(r.MessageID, r.InReplyTo)
		}
	}
	groups := map[string][]remoteMail{}
	for _, r := range records {
		groups[root(r.MessageID)] = append(groups[root(r.MessageID)], r)
	}
	var out []Thread
	usedIDs := map[int]bool{}
	for group, rs := range groups {
		sort.SliceStable(rs, func(i, j int) bool { return rs[i].Received.After(rs[j].Received) })
		folders := map[string]bool{}
		for _, r := range rs {
			folders[r.Ref.Folder] = true
		}
		for folder := range folders {
			t := Thread{ID: stableMailID(key + folder + group), Account: index, Folder: folder, Subject: rs[0].Subject, Remote: true}
			seen := map[string]bool{}
			for _, r := range rs {
				t.Refs = append(t.Refs, r.Ref)
				if r.Ref.Folder == folder {
					t.Unread = t.Unread || !r.Seen
					t.Starred = t.Starred || r.Starred
				}
				if !seen[r.MessageID] {
					t.Messages = append(t.Messages, Message{r.Sender, r.From, r.Body, r.Received})
					t.RemoteIDs = append(t.RemoteIDs, r.MessageID)
					seen[r.MessageID] = true
				}
			}
			for _, previous := range old {
				if previous.Account == index && previous.Folder == folder && previous.Remote && !previous.LocalDraft {
					match := false
					for _, id := range previous.RemoteIDs {
						if seen[id] {
							match = true
							break
						}
					}
					if match {
						t.ID = previous.ID
						break
					}
				}
			}
			for usedIDs[t.ID] {
				t.ID = stableMailID(key + folder + group + fmt.Sprint(t.ID))
			}
			usedIDs[t.ID] = true
			out = append(out, t)
		}
	}
	return out
}
func (m *model) saveLiveDraft() bool {
	d := m.compose
	if d == nil {
		return false
	}
	key := m.liveKey(d.account)
	a := copyAccount(m.live.disk.Accounts[key])
	id := d.savedID
	if id == 0 {
		id = stableMailID(opaqueID())
	}
	sd := storedDraft{ID: id, ReplyID: d.replyID, To: d.to, Subject: d.subject, Body: d.body, References: append([]string{}, d.references...), Updated: time.Now()}
	found := false
	for i := range a.Drafts {
		if a.Drafts[i].ID == id {
			a.Drafts[i] = sd
			found = true
		}
	}
	if !found {
		a.Drafts = append(a.Drafts, sd)
	}
	if !m.commitMail(key, a) {
		return false
	}
	d.savedID = id
	d.savedBody = d.body
	d.savedTo = d.to
	d.savedSubject = d.subject
	m.rebuildLive()
	m.notice = "Draft saved on this device · Ctrl+Y sends"
	return true
}
func (m *model) startLiveCompose() {
	if !m.realAccount(m.account) {
		m.notice = "Select a configured account to write new mail."
		return
	}
	m.compose = &draft{account: m.account}
	m.insert = false
	m.reading = false
	m.bodyCursor = 0
	m.composeScroll = 0
	m.replyThreadFocus = false
	m.undo = nil
	m.redo = nil
	m.editorReturnID = 0
	m.prompt = ""
	m.input = ""
	m.notice = "New message · :to email@example.com · :subject Subject · i write · Ctrl+Y send"
}
func (m *model) prepareLiveReply(t *Thread) {
	d := m.compose
	if d == nil || !m.realAccount(d.account) {
		return
	}
	if t.LocalDraft {
		d.replyID = t.ReplyID
		d.references = append([]string{}, t.References...)
	} else {
		a := m.live.disk.Accounts[m.liveKey(d.account)]
		for _, id := range t.RemoteIDs {
			var chosen *remoteMail
			for i := range a.Messages {
				r := &a.Messages[i]
				if r.MessageID == id {
					chosen = r
					break
				}
			}
			if chosen == nil {
				continue
			}
			r := chosen
			d.replyID = r.MessageID
			d.references = append(append([]string{}, r.References...), r.MessageID)
			if strings.EqualFold(r.From, m.accounts[d.account].Address) {
				d.to = r.To
			} else {
				d.to = r.ReplyAddress
				if d.to == "" {
					d.to = r.From
				}
				break
			}
		}
	}
	d.savedTo, d.savedSubject = d.to, d.subject
	m.notice = "Ctrl+Y send · Ctrl+S save · :to recipient · :subject subject"
	for _, o := range m.live.disk.Accounts[m.liveKey(d.account)].Outgoing {
		if o.DraftID == d.savedID && d.savedID != 0 && o.State == "uncertain" {
			m.notice = "Delivery uncertain · check Sent before :retry-send (may duplicate)"
		}
	}
}
func (m *model) sendLive(retry bool) {
	if m.live.offline {
		m.saveLiveDraft()
		m.notice = "Offline mode · draft kept locally. Restart online to send."
		return
	}
	if m.live.sending {
		return
	}
	d := m.compose
	if d == nil {
		return
	}
	key := m.liveKey(d.account)
	c := *m.accounts[d.account].Config
	a := copyAccount(m.live.disk.Accounts[key])
	var previous *outgoingMail
	for i := range a.Outgoing {
		if a.Outgoing[i].DraftID == d.savedID && d.savedID != 0 {
			previous = &a.Outgoing[i]
		}
	}
	if previous != nil && previous.State == "accepted" || previous != nil && previous.State == "sent" || previous != nil && previous.State == "filed" {
		m.notice = "This draft was already sent. R retries a missing Sent copy only."
		return
	}
	if previous != nil && previous.State == "uncertain" && !retry {
		m.notice = "Delivery uncertain. Check Sent before explicitly running :retry-send; it may deliver a duplicate."
		return
	}
	if !m.saveLiveDraft() {
		return
	}
	a = copyAccount(m.live.disk.Accounts[key])
	o := outgoingMail{ID: "<" + opaqueID() + "@hane.local>", DraftID: d.savedID, To: d.to, Subject: d.subject, Body: d.body, ReplyID: d.replyID, References: append([]string{}, d.references...), Date: time.Now(), State: "sending"}
	if previous != nil && previous.State == "uncertain" {
		if previous.To != d.to || previous.Subject != d.subject || previous.Body != d.body {
			m.notice = "The uncertain message has been edited. Check delivery before changing or retrying it."
			return
		}
		o = *previous
		o.State = "sending"
		o.Error = ""
	}
	wire, _, err := makeWire(c, o)
	if err != nil {
		m.notice = err.Error()
		return
	}
	o.Wire = wire
	filtered := a.Outgoing[:0]
	for _, v := range a.Outgoing {
		if v.ID != o.ID {
			filtered = append(filtered, v)
		}
	}
	a.Outgoing = append(filtered, o)
	if !m.commitMail(key, a) {
		return
	}
	m.live.sending = true
	m.notice = "Sending… waiting for the mail server to confirm acceptance."
	if previous != nil && previous.State == "uncertain" {
		o.State = "retry"
	}
	backend, store, sessionPassword := m.live.backend, m.credentials, m.live.passwords[key]
	parent := m.background
	if parent == nil {
		parent = context.Background()
	}
	m.live.pending = append(m.live.pending, func() tea.Msg {
		ctx, cancel := context.WithTimeout(parent, 4*time.Minute)
		defer cancel()
		p := sessionPassword
		var err error
		if p == "" {
			if c.RememberPassword && store != nil {
				p, err = store.Get(ctx, c)
			} else {
				err = errors.New("Sign in before sending. Draft retained.")
			}
		}
		if err != nil {
			return liveSendMsg{key, o.ID, deliveryResult{Err: err, Uncertain: o.State == "retry"}}
		}
		result := backend.Send(ctx, c, p, o)
		if o.State == "retry" && !result.Accepted {
			result.Uncertain = true
		}
		return liveSendMsg{key, o.ID, result}
	})
}
func (m *model) finishLiveSend(msg liveSendMsg) {
	m.live.sending = false
	a := copyAccount(m.live.disk.Accounts[msg.Key])
	index := -1
	for i := range m.accounts {
		if m.liveKey(i) == msg.Key {
			index = i
		}
	}
	if index < 0 {
		return
	}
	var sent *outgoingMail
	for i := range a.Outgoing {
		if a.Outgoing[i].ID == msg.ID {
			sent = &a.Outgoing[i]
			break
		}
	}
	if sent == nil {
		return
	}
	if msg.Result.Accepted {
		sent.State = "accepted"
		sent.Error = ""
		drafts := a.Drafts[:0]
		for _, d := range a.Drafts {
			if d.ID != sent.DraftID {
				drafts = append(drafts, d)
			}
		}
		a.Drafts = drafts
		if !m.commitMail(msg.Key, a) {
			m.live.disk.Accounts[msg.Key] = a
			m.notice = "Message accepted, but the local receipt could not be saved. Do not resend; fix local storage."
		} else {
			m.notice = "Message sent · saving a copy to Sent…"
		}
		if m.compose != nil && m.compose.savedID == sent.DraftID {
			m.closeCompose()
		}
		m.rebuildLive()
		m.queueSync(index, syncRequest{Folder: m.folder})
	} else {
		sent.State = "failed"
		if msg.Result.Uncertain {
			sent.State = "uncertain"
		}
		if msg.Result.Err != nil {
			sent.Error = msg.Result.Err.Error()
		}
		if m.commitMail(msg.Key, a) {
			m.notice = sent.Error + " Draft retained."
		}
	}
}

// Wrap the existing UI reducer so every navigation route (including mouse and
// Vim prefixes) schedules the same live operations and draft autosave.
func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	if m.live == nil {
		return m.updateUI(msg)
	}
	m.now = time.Now()
	oldAccount, oldFolder := m.account, m.folder
	before := ""
	if m.compose != nil {
		before = m.compose.body + "\x00" + m.compose.to + "\x00" + m.compose.subject
	}
	var cmd tea.Cmd
	switch v := msg.(type) {
	case liveSyncMsg:
		m.finishLiveSync(v)
	case liveSendMsg:
		m.finishLiveSend(v)
	case mailRefreshMsg:
		for i := range m.accounts {
			folder := inboxName(m.accounts[i])
			if i == m.account {
				folder = m.folder
			}
			if !m.live.busy[m.liveKey(i)] {
				m.queueSync(i, syncRequest{Folder: folder})
			}
		}
		cmd = refreshMailTick()
	case draftAutosaveMsg:
		if uint64(v) == m.live.autosave && m.compose != nil && m.realAccount(m.compose.account) && m.dirtyDraft() && !m.live.sending && !m.editorClose {
			m.saveLiveDraft()
		}
	default:
		if m.live.sending {
			switch msg.(type) {
			case tea.KeyPressMsg, tea.PasteMsg, tea.MouseClickMsg:
				return m, nil
			}
		}
		next, c := m.updateUI(msg)
		m = next.(model)
		cmd = c
	}
	if m.account != oldAccount || m.folder != oldFolder {
		m.queueSync(m.account, syncRequest{Folder: m.folder})
	}
	if m.compose != nil && m.realAccount(m.compose.account) {
		after := m.compose.body + "\x00" + m.compose.to + "\x00" + m.compose.subject
		if after != before {
			m.live.autosave++
			generation := m.live.autosave
			m.live.pending = append(m.live.pending, tea.Tick(750*time.Millisecond, func(time.Time) tea.Msg { return draftAutosaveMsg(generation) }))
		}
	}
	cmds := append([]tea.Cmd{cmd}, m.live.pending...)
	m.live.pending = nil
	return m, tea.Batch(cmds...)
}

func (m model) draftMatchesThread(d *Thread, id int) bool {
	if d.ReplyID == "" {
		return false
	}
	for _, t := range m.threads {
		if t.ID == id && t.Account == d.Account && stringIn(t.RemoteIDs, d.ReplyID) {
			return true
		}
	}
	return false
}

func (m *model) syncOnce(email string) error {
	found := false
	for i := range m.accounts {
		if !m.realAccount(i) || (email != "" && !strings.EqualFold(m.accounts[i].Address, email)) {
			continue
		}
		found = true
		m.queueSync(i, syncRequest{Folder: inboxName(m.accounts[i])})
		if len(m.live.pending) == 0 {
			return errors.New(m.notice)
		}
		for step := 0; len(m.live.pending) > 0; step++ {
			if step >= 8 {
				return errors.New("Sync still has pending changes; run hane sync again.")
			}
			cmd := m.live.pending[0]
			m.live.pending = m.live.pending[1:]
			result := cmd().(liveSyncMsg)
			m.finishLiveSync(result)
			if result.Result.Err != nil {
				return result.Result.Err
			}
		}
		a := m.live.disk.Accounts[m.liveKey(i)]
		if len(a.Trash) > 0 {
			return errors.New("Trash moves remain pending. Open the app to review them.")
		}
		fmt.Printf("Account %d: %d folders, %d cached messages, %d drafts; sync complete.\n", i+1, len(a.Folders), len(a.Messages), len(a.Drafts))
	}
	if !found {
		return errors.New("No matching configured account.")
	}
	return nil
}

func (m model) liveUnread(index int, folder string) (int, bool) {
	if folder == localDraftFolder || folder == syncQueueFolder {
		return 0, true
	}
	a := m.live.disk.Accounts[m.liveKey(index)]
	for _, f := range a.Folders {
		if f.Name == folder {
			_, unread := hiddenFolderCounts(a, folder)
			return max(0, int(f.Unread)-unread), true
		}
	}
	return 0, false
}

func (m model) verifyMailConnection(email string) error {
	count := 0
	for _, a := range m.accounts {
		if a.Config == nil || (email != "" && !strings.EqualFold(a.Address, email)) {
			continue
		}
		ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
		p, err := m.credentials.Get(ctx, *a.Config)
		if err == nil {
			_, err = checkAccount(ctx, *a.Config, p)
		}
		cancel()
		if err != nil {
			return err
		}
		count++
		fmt.Println("IMAP and SMTP sign-in verified; no mail sent.")
	}
	if count == 0 {
		return errors.New("No matching configured account.")
	}
	return nil
}

func (m model) folderProgress() string {
	if m.folder == syncQueueFolder {
		return "Pending Trash moves"
	}
	if m.folder == localDraftFolder {
		return "Saved on this device"
	}
	a := m.live.disk.Accounts[m.liveKey(m.account)]
	for _, f := range a.Folders {
		if f.Name == m.folder {
			hidden, _ := hiddenFolderCounts(a, m.folder)
			loaded, total := max(0, f.Loaded-hidden), max(0, int(f.Total)-hidden)
			if f.Complete {
				return fmt.Sprintf("%d cached", loaded)
			}
			return fmt.Sprintf("%d/%d cached · L older", loaded, total)
		}
	}
	return ""
}
