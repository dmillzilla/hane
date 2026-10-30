package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/emersion/go-imap"
)

type stubMailTransport struct {
	sendCount, copyCount int
	result               deliveryResult
	sent                 outgoingMail
}

func (*stubMailTransport) Sync(_ context.Context, _ AccountConfig, _ string, a accountMail, _ syncRequest) syncResult {
	return syncResult{Data: a}
}
func (s *stubMailTransport) Send(_ context.Context, _ AccountConfig, _ string, o outgoingMail) deliveryResult {
	s.sendCount++
	s.sent = o
	return s.result
}
func (s *stubMailTransport) FileSent(context.Context, AccountConfig, string, accountMail, outgoingMail) error {
	s.copyCount++
	return nil
}
func liveTestModel(t *testing.T) model {
	t.Helper()
	m := newModel(time.Now())
	c := credentialConfig(t)
	c.Folders = []string{"INBOX", "Sent"}
	m.appendConfiguredAccount(c, false)
	if err := m.initLive(filepath.Join(t.TempDir(), "mail.json")); err != nil {
		t.Fatal(err)
	}
	m.live.backend = &stubMailTransport{}
	m.live.passwords[m.liveKey(0)] = "test-password"
	a := accountMail{Folders: []folderInfo{{Name: "INBOX", Total: 1, Unread: 1, Validity: 7}, {Name: "Sent", Role: imap.SentAttr, Validity: 8}}, Messages: []remoteMail{{Ref: mailRef{"INBOX", 7, 41}, MessageID: "<root@example.com>", Subject: "A question", From: "friend@example.com", Sender: "Friend", ReplyAddress: "reply@example.com", Body: "First message", BodyLoaded: true, Received: time.Now(), Starred: true}}}
	if !m.commitMail(m.liveKey(0), a) {
		t.Fatal(m.notice)
	}
	m.rebuildLive()
	return m
}
func TestRealDraftSurvivesRestartAndUsesReplyTo(t *testing.T) {
	m := liveTestModel(t)
	m.startReply()
	if m.compose.to != "reply@example.com" || m.compose.replyID != "<root@example.com>" {
		t.Fatal("reply must use Reply-To and real message ID")
	}
	m.compose.body = "Persistent reply"
	if !m.saveDraft() {
		t.Fatal(m.notice)
	}
	id := m.compose.savedID
	m.closeCompose()
	restored := newModel(time.Now())
	restored.appendConfiguredAccount(*m.accounts[0].Config, false)
	if err := restored.initLive(m.live.path); err != nil {
		t.Fatal(err)
	}
	restored.startReply()
	if restored.compose == nil || restored.compose.body != "Persistent reply" || restored.compose.savedID != id {
		t.Fatal("reply did not restore across launches")
	}
	info, err := os.Stat(m.live.path)
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatal("mail must be private")
	}
	bytes, _ := os.ReadFile(m.live.path)
	if strings.Contains(string(bytes), "test-password") {
		t.Fatal("mail cache leaked credential")
	}
}
func TestNewDraftResumeKeepsSubjectAndHeaders(t *testing.T) {
	m := liveTestModel(t)
	m.startLiveCompose()
	m.editorCommand("to one@example.com, two@example.com")
	m.editorCommand("subject Original subject")
	m.compose.body = "New mail"
	if !m.saveDraft() {
		t.Fatal(m.notice)
	}
	m.closeCompose()
	m.folder = localDraftFolder
	m.selected = 0
	m.startReply()
	if m.compose.subject != "Original subject" || m.compose.to != "one@example.com, two@example.com" {
		t.Fatal("resuming new mail must not add Re: or lose recipients")
	}
}
func TestLiveSaveFailureKeepsEditorAndOldDraft(t *testing.T) {
	m := liveTestModel(t)
	m.startReply()
	m.compose.body = "saved"
	if !m.saveDraft() {
		t.Fatal(m.notice)
	}
	m.compose.body = "unsaved"
	m.live.path = t.TempDir()
	m.editorCommand("wq")
	if m.compose == nil || m.compose.savedBody != "saved" {
		t.Fatal("save failure must not dismiss editor or pretend saved")
	}
	a := m.live.disk.Accounts[m.liveKey(0)]
	if a.Drafts[0].Body != "saved" {
		t.Fatal("failed write changed saved draft")
	}
}
func TestLiveAutosaveAndCancelledClose(t *testing.T) {
	m := liveTestModel(t)
	m.startReply()
	m.compose.body = "autosaved"
	m.live.autosave = 3
	next, _ := m.Update(draftAutosaveMsg(3))
	m = next.(model)
	if m.compose.savedID == 0 || m.compose.savedBody != "autosaved" {
		t.Fatal("idle edit must be durable")
	}
	m.compose.body = "discardable"
	m.editorClose = true
	m.live.autosave = 4
	next, _ = m.Update(draftAutosaveMsg(4))
	m = next.(model)
	if m.compose.savedBody != "autosaved" {
		t.Fatal("close/discard choice must not be bypassed by autosave")
	}
}
func TestPendingReadIntentWinsOverStaleSync(t *testing.T) {
	m := liveTestModel(t)
	key := m.liveKey(0)
	m.live.busy[key] = true
	m.markLiveThread(m.current(), imap.SeenFlag, true)
	older := copyAccount(m.live.disk.Accounts[key])
	first := older.Flags[0].ID
	m.markLiveThread(m.current(), imap.SeenFlag, false)
	newer := m.live.disk.Accounts[key].Flags[0].ID
	if newer == first {
		t.Fatal("intent needs a new generation")
	}
	m.finishLiveSync(liveSyncMsg{Key: key, Request: syncRequest{Folder: "INBOX"}, Result: syncResult{Data: older, Applied: []string{first}}})
	a := m.live.disk.Accounts[key]
	if len(a.Flags) != 1 || a.Flags[0].ID != newer || a.Messages[0].Seen || !m.current().Unread {
		t.Fatal("stale response overwrote a newer unread action")
	}
	if a.Folders[0].Unread != 1 {
		t.Fatal("unread counter lost pending user intent")
	}
	if !a.Messages[0].Starred {
		t.Fatal("read action changed unrelated flag")
	}
}
func TestNoImplicitSendAndAcceptedSendRemovesOnlyItsDraft(t *testing.T) {
	m := liveTestModel(t)
	backend := m.live.backend.(*stubMailTransport)
	backend.result = deliveryResult{Accepted: true}
	m.startReply()
	m.compose.body = "Actual reply"
	m.saveDraft()
	savedID := m.compose.savedID
	if backend.sendCount != 0 {
		t.Fatal("saving must never send")
	}
	m.live.pending = nil
	m.sendLive(false)
	if !m.live.sending {
		t.Fatal(m.notice)
	}
	a, err := loadMailDisk(m.live.path)
	if err != nil {
		t.Fatal(err)
	}
	o := a.Accounts[m.liveKey(0)].Outgoing[0]
	if o.State != "uncertain" {
		t.Fatal("interrupted in-flight send must restore as uncertain")
	}
	cmd := m.live.pending[len(m.live.pending)-1]
	m.live.pending = nil
	result := cmd().(liveSendMsg)
	m.finishLiveSend(result)
	if backend.sendCount != 1 || !result.Result.Accepted || m.compose != nil {
		t.Fatal("explicit send did not complete")
	}
	a2 := m.live.disk.Accounts[m.liveKey(0)]
	if len(a2.Drafts) != 0 || a2.Outgoing[0].State != "accepted" {
		t.Fatal("SMTP acceptance must durably remove the draft and retain receipt")
	}
	if a2.Outgoing[0].DraftID != savedID || !strings.Contains(string(backend.sent.Wire), "In-Reply-To: <root@example.com>") {
		t.Fatal("reply headers or draft association missing")
	}
	if !strings.Contains(m.current().newest().Body, "Actual reply") {
		t.Fatal("sent reply must appear in original thread immediately")
	}
}
func TestUncertainSendRetainsDraftAndBlocksOrdinaryRetry(t *testing.T) {
	m := liveTestModel(t)
	backend := m.live.backend.(*stubMailTransport)
	backend.result = deliveryResult{Uncertain: true, Err: errors.New("acceptance unknown")}
	m.startReply()
	m.compose.body = "Do not duplicate"
	m.sendLive(false)
	cmd := m.live.pending[len(m.live.pending)-1]
	m.live.pending = nil
	m.finishLiveSend(cmd().(liveSendMsg))
	if m.compose == nil || len(m.live.disk.Accounts[m.liveKey(0)].Drafts) != 1 {
		t.Fatal("uncertain delivery lost draft")
	}
	m.sendLive(false)
	if backend.sendCount != 1 || m.live.sending || !strings.Contains(m.notice, "uncertain") {
		t.Fatal("ordinary send must not retry uncertain delivery")
	}
	m.live.pending = nil
	m.live.busy[m.liveKey(0)] = false
	m.queueSync(0, syncRequest{Folder: "INBOX"})
	sync := m.live.pending[0]().(liveSyncMsg)
	m.finishLiveSync(sync)
	if backend.sendCount != 1 || backend.copyCount != 0 {
		t.Fatal("background sync retried uncertain transmission or filed it as sent")
	}
}
func TestThreadingAcrossFoldersDeduplicatesCopiesAndKeepsAccountIsolation(t *testing.T) {
	now := time.Now()
	rs := []remoteMail{
		{Ref: mailRef{"Archive", 1, 1}, MessageID: "<root@test>", Body: "root", Received: now.Add(-time.Hour)},
		{Ref: mailRef{"INBOX", 2, 9}, MessageID: "<reply@test>", References: []string{"<root@test>"}, Body: "reply", Received: now},
		{Ref: mailRef{"Sent", 3, 8}, MessageID: "<reply@test>", References: []string{"<root@test>"}, Body: "reply", Received: now},
		{Ref: mailRef{"INBOX", 2, 10}, MessageID: "<unrelated@test>", Subject: "same subject", Body: "separate", Received: now},
	}
	threads := buildMailThreads(0, "account-a", rs, nil)
	for _, thread := range threads {
		if stringIn(thread.RemoteIDs, "<root@test>") {
			if len(thread.Messages) != 2 || thread.newest().Body != "reply" {
				t.Fatal("full thread ordering or duplicate suppression failed")
			}
		}
	}
	other := buildMailThreads(1, "account-b", rs, nil)
	for _, a := range threads {
		for _, b := range other {
			if a.ID == b.ID {
				t.Fatal("thread ID leaked across account")
			}
		}
	}
}
func TestLiveSendKeyCannotEditDuringSubmission(t *testing.T) {
	m := liveTestModel(t)
	m.startReply()
	m.compose.body = "original"
	m.live.sending = true
	m.insert = true
	next, _ := m.Update(tea.PasteMsg{Content: "alteration"})
	if next.(model).compose.body != "original" {
		t.Fatal("in-flight body changed")
	}
}

func TestLoadedPageSizeRestoresAcrossLaunches(t *testing.T) {
	m := liveTestModel(t)
	key := m.liveKey(0)
	a := m.live.disk.Accounts[key]
	a.Folders[0].Loaded = 250
	m.live.disk.Accounts[key] = a
	m.queueSync(0, syncRequest{Folder: "INBOX"})
	if m.live.limits[key+"/INBOX"] != 250 {
		t.Fatal("restart would forget the downloaded page size")
	}
}
func TestRealEditorLayoutsAndShortcuts(t *testing.T) {
	m := liveTestModel(t)
	for _, newMessage := range []bool{false, true} {
		if newMessage {
			m.startLiveCompose()
		} else {
			m.startReply()
		}
		for _, w := range []int{48, 80, 120, 150} {
			m.width = w
			m.height = 32
			lines := strings.Split(m.View().Content, "\n")
			if len(lines) != 32 {
				t.Fatalf("%d: wrong view height", w)
			}
			if newMessage {
				left, right := m.replyWidths()
				if left != 0 || right != w {
					t.Fatal("new compose should fill the pane")
				}
			}
			if c := m.replyCursor(); c != nil && (c.X < 0 || c.X >= w || c.Y < 0 || c.Y >= 32) {
				t.Fatalf("%d: cursor outside terminal", w)
			}
		}
		m.closeCompose()
	}
}

func TestOfflineModeQueuesReadIntentButNeverSubmits(t *testing.T) {
	m := liveTestModel(t)
	m.live.offline = true
	m.openThread()
	if len(m.live.pending) != 0 || len(m.live.disk.Accounts[m.liveKey(0)].Flags) == 0 {
		t.Fatal("offline reads must persist intent without network commands")
	}
	m.startReply()
	m.compose.body = "Send when back online"
	m.sendLive(false)
	if m.live.sending || len(m.live.pending) != 0 || len(m.live.disk.Accounts[m.liveKey(0)].Drafts) != 1 {
		t.Fatal("offline send must only retain a local draft")
	}
}
func TestSentReceiptStopsRenderingAfterServerCopyIsObserved(t *testing.T) {
	m := liveTestModel(t)
	key := m.liveKey(0)
	a := copyAccount(m.live.disk.Accounts[key])
	a.Outgoing = []outgoingMail{{ID: "<sent@test>", State: "sent", Body: "sent", Date: time.Now()}}
	a.Messages = append(a.Messages, remoteMail{Ref: mailRef{"Sent", 8, 2}, MessageID: "<sent@test>", Body: "sent", Received: time.Now(), Seen: true, BodyLoaded: true})
	m.live.disk.Accounts[key] = a
	m.finishLiveSync(liveSyncMsg{Key: key, Result: syncResult{Data: copyAccount(a)}})
	if m.live.disk.Accounts[key].Outgoing[0].State != "filed" {
		t.Fatal("server copy did not retire the synthetic sent message")
	}
	a = copyAccount(m.live.disk.Accounts[key])
	a.Messages = a.Messages[:1]
	m.finishLiveSync(liveSyncMsg{Key: key, Result: syncResult{Data: a}})
	for _, thread := range m.threads {
		if stringIn(thread.RemoteIDs, "<sent@test>") {
			t.Fatal("deleted sent message resurrected from delivery journal")
		}
	}
}

func TestRejectedRetryDoesNotEraseEarlierDeliveryUncertainty(t *testing.T) {
	m := liveTestModel(t)
	backend := m.live.backend.(*stubMailTransport)
	backend.result = deliveryResult{Uncertain: true, Err: errors.New("unknown acceptance")}
	m.startReply()
	m.compose.body = "Still uncertain"
	m.sendLive(false)
	cmd := m.live.pending[len(m.live.pending)-1]
	m.live.pending = nil
	m.finishLiveSend(cmd().(liveSendMsg))
	backend.result = deliveryResult{Err: errors.New("offline before retry")}
	m.sendLive(true)
	cmd = m.live.pending[len(m.live.pending)-1]
	m.live.pending = nil
	m.finishLiveSend(cmd().(liveSendMsg))
	a := m.live.disk.Accounts[m.liveKey(0)]
	if a.Outgoing[len(a.Outgoing)-1].State != "uncertain" {
		t.Fatal("a failed retry must not clear the original uncertainty")
	}
	m.sendLive(false)
	if m.live.sending {
		t.Fatal("normal send allowed after an uncertain retry failed")
	}
}
