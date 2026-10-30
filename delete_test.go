package main

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/emersion/go-imap"
)

func trashTestModel(t *testing.T) model {
	m := liveTestModel(t)
	m.live.offline = true
	key := m.liveKey(0)
	a := copyAccount(m.live.disk.Accounts[key])
	a.Folders = append(a.Folders, folderInfo{Name: "Deleted Messages", Role: imap.TrashAttr, Validity: 9})
	a.Messages = append(a.Messages, remoteMail{Ref: mailRef{"Sent", 8, 55}, MessageID: "<sent@example.com>", InReplyTo: a.Messages[0].MessageID, References: []string{a.Messages[0].MessageID}, Subject: "Re: A question", From: m.accounts[0].Address, To: "friend@example.com", Body: "Earlier reply", BodyLoaded: true, Received: time.Now().Add(-time.Hour), Seen: true})
	if !m.commitMail(key, a) {
		t.Fatal(m.notice)
	}
	m.rebuildLive()
	return m
}
func TestDeleteConfirmationRoutesAndScope(t *testing.T) {
	routes := map[string]func(model) model{
		"dd":      func(m model) model { return press(m, "dd") },
		"Delete":  func(m model) model { return special(m, tea.KeyDelete) },
		":delete": func(m model) model { return special(press(m, ":delete"), tea.KeyEnter) },
	}
	for name, request := range routes {
		t.Run(name, func(t *testing.T) {
			m := trashTestModel(t)
			m.startReply()
			m.compose.body = "Keep this draft"
			m.saveDraft()
			m.closeCompose()
			m.reading = true
			m.focus = threadPane
			m = request(m)
			if m.deletion == nil || m.deletion.selected || len(m.deletion.refs) != 1 || m.deletion.refs[0].Folder != "INBOX" {
				t.Fatal("must confirm exactly the cached messages from the current folder")
			}
			next, _ := m.Update(tea.PasteMsg{Content: "unwanted"})
			m = next.(model)
			m = special(m, tea.KeyEnter)
			if m.deletion != nil || len(m.live.disk.Accounts[m.liveKey(0)].Trash) != 0 || !m.reading {
				t.Fatal("Cancel must leave the open mail unchanged")
			}
			m = request(m)
			m = special(press(m, "l"), tea.KeyEnter)
			a := m.live.disk.Accounts[m.liveKey(0)]
			if m.deletion != nil || m.reading || len(a.Trash) != 1 || len(a.Drafts) != 1 || len(a.Messages) != 2 {
				t.Fatal("confirmation should queue the move, retaining mail and draft until sync")
			}
			if m.current() != nil {
				t.Fatal("confirmed mail must disappear immediately")
			}
			disk, err := loadMailDisk(m.live.path)
			if err != nil || len(disk.Accounts[m.liveKey(0)].Trash) != 1 {
				t.Fatal("offline intent must survive restart")
			}
			m = request(m)
			if m.deletion != nil || len(a.Trash) != 1 {
				t.Fatal("must not queue the same UID twice")
			}
		})
	}
}
func TestDeleteDraftAndSaveFailure(t *testing.T) {
	m := trashTestModel(t)
	m.startReply()
	m.compose.body = "Draft"
	m.saveDraft()
	m.closeCompose()
	m.folder = localDraftFolder
	m.selected = 0
	m = press(m, "ddl")
	m = special(m, tea.KeyEnter)
	if len(m.live.disk.Accounts[m.liveKey(0)].Drafts) != 0 || len(m.live.disk.Accounts[m.liveKey(0)].Trash) != 0 {
		t.Fatal("local draft deletion should not become a server move")
	}
	m.folder = "INBOX"
	m.live.path = filepath.Join(m.live.path, "impossible.json")
	m = press(m, "ddl")
	m = special(m, tea.KeyEnter)
	if m.deletion == nil || len(m.live.disk.Accounts[m.liveKey(0)].Trash) != 0 || !strings.Contains(m.notice, "storage failed") {
		t.Fatal("storage failure must retain confirmation and mail")
	}
}
func TestDeleteSnapshotSurvivesNewArrivalAndStaleSync(t *testing.T) {
	m := trashTestModel(t)
	key := m.liveKey(0)
	old := copyAccount(m.live.disk.Accounts[key])
	m = press(m, "dd")
	target := m.deletion.refs[0]
	newer := copyAccount(old)
	newer.Messages = append(newer.Messages, remoteMail{Ref: mailRef{"INBOX", 7, 99}, MessageID: "<new@example.com>", Subject: "New arrival", Received: time.Now().Add(time.Hour), BodyLoaded: true})
	m.finishLiveSync(liveSyncMsg{Key: key, Result: syncResult{Data: newer}})
	m = special(press(m, "l"), tea.KeyEnter)
	queued := m.live.disk.Accounts[key].Trash[0]
	if len(queued.Refs) != 1 || queued.Refs[0] != target {
		t.Fatal("new arrival must not replace the confirmed target")
	}
	m.finishLiveSync(liveSyncMsg{Key: key, Result: syncResult{Data: old, Err: errors.New("offline")}})
	if len(m.live.disk.Accounts[key].Trash) != 1 {
		t.Fatal("stale/failed sync dropped newer move intent")
	}
	result := copyAccount(old)
	removeMovedCache(&result, queued.Refs)
	m.finishLiveSync(liveSyncMsg{Key: key, Result: syncResult{Data: result, TrashApplied: []string{queued.ID}}})
	a := m.live.disk.Accounts[key]
	if len(a.Trash) != 0 || len(a.Messages) != 1 || a.Messages[0].Ref.Folder != "Sent" {
		t.Fatal("successful move should remove only confirmed source records")
	}
}
func TestDeleteModalNavigationGeometryAndIsolation(t *testing.T) {
	for _, width := range []int{48, 80, 120} {
		m := press(newModel(time.Now()), "dd")
		m.width = width
		m.height = 24
		v := m.View().Content
		for _, line := range strings.Split(v, "\n") {
			if ansi.StringWidth(line) != width {
				t.Fatalf("modal overflow at %d columns", width)
			}
		}
		m = press(m, "jk")
		if m.deletion.selected {
			t.Fatal("k should select Cancel")
		}
		m = special(m, tea.KeyEscape)
		if m.deletion != nil {
			t.Fatal("Esc should dismiss")
		}
		m.focus = treePane
		m = press(m, "dd")
		if m.deletion != nil {
			t.Fatal("dd in tree must not delete hidden mail")
		}
	}
	m := press(newModel(time.Now()), "dd")
	id := m.current().ID
	x, y, _, _ := m.deleteGeometry()
	bx, by := m.deleteButtons()
	next, _ := m.Update(tea.MouseClickMsg{X: x + bx + 14, Y: y + by, Button: tea.MouseLeft})
	m = next.(model)
	for _, th := range m.threads {
		if th.ID == id && th.Folder != "Trash" {
			t.Fatal("mouse confirm did not move demo mail")
		}
	}
	m = press(newModel(time.Now()), "ri")
	m = press(m, "dd")
	if m.deletion != nil || m.compose.body != "dd" {
		t.Fatal("dd in Insert must remain text")
	}
}
func TestInterruptedTrashCopyNeverRetriesAutomatically(t *testing.T) {
	m := trashTestModel(t)
	key := m.liveKey(0)
	a := copyAccount(m.live.disk.Accounts[key])
	a.Trash = []trashChange{{ID: "copy", Destination: "Deleted Messages", Refs: []mailRef{a.Messages[0].Ref}, State: "copying"}}
	if !m.commitMail(key, a) {
		t.Fatal(m.notice)
	}
	disk, err := loadMailDisk(m.live.path)
	if err != nil {
		t.Fatal(err)
	}
	op := disk.Accounts[key].Trash[0]
	if op.State != "uncertain" || trashWorkPending([]trashChange{op}) {
		t.Fatal("interrupted COPY must require review")
	}
	m.live.disk = disk
	m.rebuildLive()
	m.openSyncQueue()
	m.cancelUncertainDelete()
	if len(m.live.disk.Accounts[key].Trash) != 0 || len(m.live.disk.Accounts[key].Messages) != 2 {
		t.Fatal("clearing uncertain attempt must keep source mail")
	}
}

func TestDeletingSentMailRetiresPendingFilingReceipt(t *testing.T) {
	m := trashTestModel(t)
	key := m.liveKey(0)
	a := copyAccount(m.live.disk.Accounts[key])
	a.Outgoing = []outgoingMail{{ID: "<sent@example.com>", State: "accepted", Wire: []byte("fictional wire")}}
	if !m.commitMail(key, a) {
		t.Fatal(m.notice)
	}
	m.rebuildLive()
	m.folder = "Sent"
	m.selected = 0
	m = special(press(m, "ddl"), tea.KeyEnter)
	a = m.live.disk.Accounts[key]
	if len(a.Trash) != 1 || a.Outgoing[0].State != "filed" {
		t.Fatal("pending Sent filing must not recreate deliberately trashed mail")
	}
}
