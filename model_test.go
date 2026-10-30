package main

import (
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
)

func press(m model, keys string) model {
	for _, r := range keys {
		next, _ := m.Update(tea.KeyPressMsg{Code: r, Text: string(r)})
		m = next.(model)
	}
	return m
}
func special(m model, code rune) model {
	next, _ := m.Update(tea.KeyPressMsg{Code: code})
	return next.(model)
}

func TestMailOrderingAndAccountIsolation(t *testing.T) {
	m := newModel(time.Now())
	if len(m.accounts) != 2 {
		t.Fatal("expected two accounts")
	}
	for a := range m.accounts {
		for _, f := range m.accounts[a].Folders {
			m.account = a
			m.folder = f
			list := m.folderThreads()
			for i, thread := range list {
				if thread.Account != a {
					t.Fatal("mail leaked across accounts")
				}
				if i > 0 && list[i-1].newest().Received.Before(thread.newest().Received) {
					t.Fatal("inbox is not newest first")
				}
				for j := 1; j < len(thread.Messages); j++ {
					if thread.Messages[j-1].Received.Before(thread.Messages[j].Received) {
						t.Fatal("thread is not newest first")
					}
				}
			}
		}
	}
}
func TestNeovimNavigation(t *testing.T) {
	m := newModel(time.Now())
	m = press(m, "\\nt")
	if m.treeOpen {
		t.Fatal("leader nt should close tree")
	}
	m = press(m, "\\nt")
	if !m.treeOpen || m.focus != treePane {
		t.Fatal("leader nt should open and focus tree")
	}
	// Select Studio / Inbox by its actual tree position.
	for i, r := range m.rows() {
		if r.account == 1 && r.folder == "Inbox" {
			m.treeCursor = i
		}
	}
	m = press(m, "l")
	if m.account != 1 || m.focus != listPane {
		t.Fatal("folder selection failed")
	}
	m = press(m, "jjl")
	if m.selected != 2 || !m.reading || m.focus != threadPane {
		t.Fatal("message selection failed")
	}
	m = press(m, "G")
	if m.scroll == 0 {
		t.Fatal("G did not scroll the entire thread")
	}
	m = press(m, "gg")
	if m.scroll != 0 {
		t.Fatal("gg did not return to latest reply")
	}
	m = press(m, "h")
	if m.reading || m.focus != listPane {
		t.Fatal("h did not return to inbox")
	}
	m = press(m, "G")
	if m.selected != len(m.folderThreads())-1 || m.listOffset == 0 {
		t.Fatal("list did not scroll to final message")
	}
}
func TestSearchAndEmptyFolder(t *testing.T) {
	m := newModel(time.Now())
	m = press(m, "/lighthouse")
	m = special(m, tea.KeyEnter)
	if len(m.folderThreads()) != 1 {
		t.Fatal("search should find text inside the message body")
	}
	m = special(m, tea.KeyEscape)
	if len(m.folderThreads()) != 8 {
		t.Fatal("escape should clear search")
	}
	m.folder = "Drafts"
	m = press(m, "ljkGr")
	if m.compose != nil || m.current() != nil {
		t.Fatal("empty folder should be safe")
	}
}
func TestReplyDraftRoundTrip(t *testing.T) {
	m := newModel(time.Now())
	m.account = 1
	originalID := m.current().ID
	recipient := m.current().newest().Address
	oldCount := len(m.current().Messages)
	m = press(m, "riHello from the studio.")
	m = special(m, tea.KeyEscape)
	m = press(m, ":wq")
	m = special(m, tea.KeyEnter)
	if m.compose != nil || m.count(1, "Drafts") != 1 || m.count(0, "Drafts") != 0 {
		t.Fatal("draft stored in wrong account")
	}
	m.folder = "Drafts"
	m.selected = 0
	m = press(m, "ri")
	if m.compose.to != recipient || m.compose.threadID != originalID {
		t.Fatal("draft lost original recipient or thread")
	}
	m = press(m, " More detail.")
	m.saveDraft()
	m.closeCompose()
	if m.count(1, "Drafts") != 1 {
		t.Fatal("saving existing draft duplicated it")
	}
	m = press(m, "ri")
	m = special(m, tea.KeyEscape)
	m = press(m, ":send")
	m = special(m, tea.KeyEnter)
	if m.compose != nil || m.count(1, "Drafts") != 0 {
		t.Fatal("sent draft was not removed")
	}
	m.folder = "Inbox"
	for _, th := range m.folderThreads() {
		if th.ID == originalID {
			if len(th.Messages) != oldCount+1 || th.newest().Address != m.accounts[1].Address || !strings.Contains(th.newest().Body, "More detail.") {
				t.Fatal("reply did not join original thread with correct sender")
			}
			return
		}
	}
	t.Fatal("original thread missing")
}
func TestUnicodeReplyEditing(t *testing.T) {
	m := newModel(time.Now())
	m = press(m, "riこんにちは🌊")
	m = special(m, tea.KeyBackspace)
	if m.compose.body != "こんにちは" {
		t.Fatal("backspace damaged Unicode")
	}
	next, _ := m.Update(tea.PasteMsg{Content: "\nA pasted paragraph."})
	m = next.(model)
	if !strings.Contains(m.compose.body, "\nA pasted paragraph.") {
		t.Fatal("multiline paste failed")
	}
}
func TestMouseOpensCorrectMessage(t *testing.T) {
	m := newModel(time.Now())
	tw, _, _ := m.layout()
	next, _ := m.Update(tea.MouseClickMsg{X: tw + 4, Y: 13, Button: tea.MouseLeft})
	m = next.(model)
	if m.selected != 1 || m.focus != threadPane || !m.reading {
		t.Fatal("click did not select second message")
	}
}

func TestNarrowExplorerToggle(t *testing.T) {
	m := newModel(time.Now())
	m.width = 80
	m = press(m, "\\nt")
	tw, _, _ := m.layout()
	if tw != 80 || m.focus != treePane {
		t.Fatal("first toggle should reveal the hidden drawer in a narrow terminal")
	}
	m = press(m, "\\nt")
	tw, _, _ = m.layout()
	if tw != 0 || m.focus == treePane {
		t.Fatal("second toggle should dismiss the drawer")
	}
}
func TestViewFitsTerminal(t *testing.T) {
	for _, size := range [][2]int{{160, 44}, {120, 36}, {90, 24}, {80, 24}, {48, 16}, {30, 10}} {
		for _, state := range []string{"inbox", "thread", "tree", "splash", "reply", "reply thread", "help", "quit", "editor help", "editor close"} {
			m := newModel(time.Now())
			m.width = size[0]
			m.height = size[1]
			switch state {
			case "editor help":
				m.startReply()
				m.editorHelp = true
			case "editor close":
				m.startReply()
				m.insertText("Unsaved text")
				m.requestEditorClose()
			case "quit":
				m.requestQuit()
			case "thread":
				m.openThread()
			case "tree":
				m.focus = treePane
			case "splash":
				m.loading = true
			case "reply thread":
				m.startReply()
				m.focusReplyPane(true)
			case "reply":
				m.startReply()
			case "help":
				m.help = true
			}
			m.normalizeSelection()
			lines := strings.Split(m.View().Content, "\n")
			if len(lines) != m.height {
				t.Fatalf("%s %v: wrong height %d", state, size, len(lines))
			}
			for i, line := range lines {
				if ansi.StringWidth(line) != m.width {
					t.Fatalf("%s %v line %d: width %d", state, size, i, ansi.StringWidth(line))
				}
			}
		}
	}
}
