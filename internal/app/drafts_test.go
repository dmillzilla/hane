package app

import (
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
)

func TestSaveCloseResumesFromOriginalConversation(t *testing.T) {
	for _, account := range []int{0, 1} {
		m := newModel(time.Now())
		m.account = account
		id, originalBody := m.current().ID, m.current().newest().Body
		m = press(m, "lriKeep this reply")
		m = editorCmd(special(m, tea.KeyEscape), "wq")
		if m.compose != nil || m.current().ID != id || !m.reading || m.current().newest().Body != originalBody {
			t.Fatal("save and close must preserve the original conversation")
		}
		saved := m.savedReply(account, id)
		if saved == nil || saved.newest().Body != "Keep this reply" || m.count(account, "Drafts") != 1 {
			t.Fatal("save and close must retain a draft in the same account")
		}
		savedID := saved.ID
		if !strings.Contains(ansi.Strip(m.View().Content), "Draft · r to resume") {
			t.Fatal("original conversation must advertise its saved draft")
		}
		for range 3 {
			m = press(m, "r")
			if m.compose.body != "Keep this reply" || m.compose.savedID != savedID || m.compose.threadID != id || m.insert {
				t.Fatal("replying again must resume the saved draft in Normal")
			}
			m = editorCmd(m, "wq")
			if m.count(account, "Drafts") != 1 {
				t.Fatal("resaving must update one draft")
			}
		}
		m.folder, m.selected, m.reading, m.focus = "Drafts", 0, false, listPane
		m = special(m, tea.KeyEnter)
		if m.compose == nil || m.compose.savedID != savedID || m.replyThread().ID != id {
			t.Fatal("Enter on a draft must reopen editor beside its original thread")
		}
		m = press(m, "q")
		if m.compose != nil || m.count(account, "Drafts") != 1 {
			t.Fatal("closing a saved draft must retain it")
		}
		if m.count(1-account, "Drafts") != 0 {
			t.Fatal("draft must not leak into other account")
		}
	}
}

func TestSendingConsumesOnlyTheSentDraft(t *testing.T) {
	for _, route := range []string{"normal shortcut", "insert shortcut", "thread shortcut", "command"} {
		t.Run(route, func(t *testing.T) {
			m := newModel(time.Now())
			m.account = 1
			m = press(m, "riOther account draft")
			m = editorCmd(special(m, tea.KeyEscape), "wq")
			m.account = 0
			id, oldCount, oldSent := m.current().ID, len(m.current().Messages), m.count(0, "Sent")
			m = press(m, "riA saved reply")
			m = editorCmd(special(m, tea.KeyEscape), "wq")
			m = press(m, "ri with final edits")
			if route != "insert shortcut" {
				m = special(m, tea.KeyEscape)
			}
			if route == "thread shortcut" {
				m = window(m, "h")
			}
			if route == "command" {
				m = editorCmd(m, "send")
			} else {
				m = control(m, 'y')
			}
			if m.compose != nil || m.savedReply(0, id) != nil || m.count(0, "Drafts") != 0 || m.count(1, "Drafts") != 1 {
				t.Fatal("sending must remove only the sent draft")
			}
			if m.count(0, "Sent") != oldSent+1 || m.current().ID != id || len(m.current().Messages) != oldCount+1 || m.current().newest().Body != "A saved reply with final edits" {
				t.Fatal("sending must append latest edits exactly once to thread and Sent")
			}
			m = press(m, "r")
			if m.compose.body != "" || m.compose.savedID != 0 {
				t.Fatal("sent draft must not reappear when replying")
			}
		})
	}
}

func TestEmptySendRetainsDraftAndShortcutIsVisible(t *testing.T) {
	m := press(newModel(time.Now()), "r")
	m = control(m, 's')
	id := m.compose.savedID
	m = control(m, 'y')
	if m.compose == nil || m.compose.savedID != id || m.count(0, "Drafts") != 1 || !strings.Contains(m.notice, "Add a message") {
		t.Fatal("rejected empty send must leave draft intact")
	}
	for _, width := range []int{48, 80, 90, 120, 150} {
		m.width = width
		for _, focus := range []string{"normal", "insert", "thread"} {
			m.insert = focus == "insert"
			m.replyThreadFocus = focus == "thread"
			lines := strings.Split(ansi.Strip(m.View().Content), "\n")
			for _, action := range []string{"send", "save", "close", "F1"} {
				if !strings.Contains(lines[len(lines)-1], action) {
					t.Fatalf("%d %s: missing %s", width, focus, action)
				}
			}
		}
	}
}
