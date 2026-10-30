package main

import (
	"errors"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
)

func TestPendingDeletionHidesImmediatelyAcrossRestartAndFailedSync(t *testing.T) {
	m := trashTestModel(t)
	key := m.liveKey(0)
	old := copyAccount(m.live.disk.Accounts[key])
	m = special(press(m, "ddl"), tea.KeyEnter)
	if len(m.folderThreads()) != 0 {
		t.Fatal("deleted mail is still visible")
	}
	if n, _ := m.liveUnread(0, "INBOX"); n != 0 {
		t.Fatal("unread count should drop immediately")
	}
	if m.live.disk.Accounts[key].Folders[0].Unread != 1 {
		t.Fatal("view filtering must not corrupt server counters")
	}
	restored := newModel(time.Now())
	restored.appendConfiguredAccount(*m.accounts[0].Config, false)
	if err := restored.initLive(m.live.path); err != nil {
		t.Fatal(err)
	}
	if len(restored.folderThreads()) != 0 {
		t.Fatal("restart resurrected pending deletion")
	}
	for i := 0; i < 2; i++ {
		m.finishLiveSync(liveSyncMsg{Key: key, Result: syncResult{Data: copyAccount(old), Err: errors.New("network unavailable")}})
		if len(m.folderThreads()) != 0 {
			t.Fatal("stale or failed sync resurrected pending deletion")
		}
		if n, _ := m.liveUnread(0, "INBOX"); n != 0 {
			t.Fatal("unread adjustment must remain stable")
		}
	}
	m.folder = "Sent"
	if len(m.folderThreads()) != 1 {
		t.Fatal("another folder lost its message")
	}
	m.folder = localDraftFolder // local drafts have independent identities
	if !stringIn(m.accounts[0].Folders, syncQueueFolder) {
		t.Fatal("queue missing from explorer")
	}
}
func TestQueueDetailsRecoveryAndNoMailActions(t *testing.T) {
	m := trashTestModel(t)
	key := m.liveKey(0)
	m = special(press(m, "ddl"), tea.KeyEnter)
	m = press(m, "Q")
	if m.folder != syncQueueFolder || m.current() == nil || m.current().QueueID == "" {
		t.Fatal("Q must open pending moves")
	}
	if !strings.Contains(ansi.Strip(m.View().Content), "Waiting for connection") {
		t.Fatal("offline queue lacks status")
	}
	m = special(m, tea.KeyEnter)
	beforeFlags := len(m.live.disk.Accounts[key].Flags)
	m = press(m, "rusdd")
	if !m.reading || m.compose != nil || m.deletion != nil || len(m.live.disk.Accounts[key].Flags) != beforeFlags {
		t.Fatal("queue entries must not act as email")
	}
	a := copyAccount(m.live.disk.Accounts[key])
	a.Trash[0].State = "uncertain"
	if !m.commitMail(key, a) {
		t.Fatal(m.notice)
	}
	m.rebuildLive()
	if !strings.Contains(ansi.Strip(m.View().Content), "Needs review") {
		t.Fatal("uncertain move lacks recovery status")
	}
	m = special(press(m, ":canceldelete"), tea.KeyEnter)
	if m.reading || len(m.folderThreads()) != 0 || len(m.live.disk.Accounts[key].Trash) != 0 {
		t.Fatal("cleared attempt must leave queue")
	}
	m.folder = "INBOX"
	if len(m.folderThreads()) != 1 {
		t.Fatal("clearing uncertain move should restore original view")
	}
	if n, _ := m.liveUnread(0, "INBOX"); n != 1 {
		t.Fatal("unread count should restore with original")
	}
}
func TestQueueKeepsFailureDetailsAndRemovesCompletedEntry(t *testing.T) {
	m := trashTestModel(t)
	key := m.liveKey(0)
	m = special(press(m, "ddl"), tea.KeyEnter)
	data := copyAccount(m.live.disk.Accounts[key])
	op := data.Trash[0]
	m.live.offline = false
	m.live.busy[key] = true
	m.finishLiveSync(liveSyncMsg{Key: key, Result: syncResult{Data: data, TrashUpdates: data.Trash, Err: errors.New("Connection timed out")}})
	m.openSyncQueue()
	m.openThread()
	content := ansi.Strip(m.View().Content)
	if !strings.Contains(content, "Retry needed") || !strings.Contains(content, "Connection timed out") {
		t.Fatal("queue should expose the failed attempt")
	}
	disk, err := loadMailDisk(m.live.path)
	if err != nil || disk.Accounts[key].Trash[0].Error == "" {
		t.Fatal("failure details should survive restart")
	}
	m.live.offline = true
	removeMovedCache(&data, op.Refs)
	m.finishLiveSync(liveSyncMsg{Key: key, Result: syncResult{Data: data, TrashApplied: []string{op.ID}}})
	if m.reading || len(m.folderThreads()) != 0 || !strings.Contains(ansi.Strip(m.View().Content), "Queue clear") {
		t.Fatal("completed move should leave queue and close its details")
	}
}
func TestPendingDeletionDoesNotHideNewUIDOrOtherAccount(t *testing.T) {
	m := trashTestModel(t)
	key := m.liveKey(0)
	m = special(press(m, "ddl"), tea.KeyEnter)
	a := copyAccount(m.live.disk.Accounts[key])
	newMail := a.Messages[0]
	newMail.Ref.UID++
	newMail.MessageID = "<new-arrival@example.com>"
	newMail.References = nil
	newMail.Received = time.Now().Add(time.Hour)
	a.Messages = append(a.Messages, newMail)
	a.Folders[0].Unread++
	if !m.commitMail(key, a) {
		t.Fatal(m.notice)
	}
	c := *m.accounts[0].Config
	c.Email = "another@example.com"
	m.appendConfiguredAccount(c, false)
	secondKey := m.liveKey(1)
	other := copyAccount(a)
	other.Trash = nil
	if !m.commitMail(secondKey, other) {
		t.Fatal(m.notice)
	}
	m.rebuildLive()
	m.account = 0
	m.folder = "INBOX"
	if len(m.folderThreads()) != 1 || !stringIn(m.current().RemoteIDs, newMail.MessageID) {
		t.Fatal("new arrival must remain visible")
	}
	m.account = 1
	m.folder = "INBOX"
	if len(m.folderThreads()) != 2 {
		t.Fatal("pending UID leaked between accounts")
	}
}
func TestQueueNavigationUsesRealFolderForSyncAndFitsTerminal(t *testing.T) {
	m := trashTestModel(t)
	key := m.liveKey(0)
	m = special(press(m, "ddl"), tea.KeyEnter)
	m = special(press(m, ":queue"), tea.KeyEnter)
	for _, width := range []int{48, 80, 120} {
		m.width = width
		m.height = 24
		m.openThread()
		for _, line := range strings.Split(m.View().Content, "\n") {
			if ansi.StringWidth(line) != width {
				t.Fatalf("queue overflow at %d columns", width)
			}
		}
	}
	m.live.offline = false
	m.live.busy[key] = true
	m.queueSync(0, syncRequest{Folder: syncQueueFolder})
	if m.live.queued[key].Folder != "INBOX" {
		t.Fatal("virtual queue must not be selected on IMAP")
	}
}
