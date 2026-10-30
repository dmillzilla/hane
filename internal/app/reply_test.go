package app

import (
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
)

func window(m model, direction string) model { return press(control(m, 'w'), direction) }

func TestReplyModesAndNativeCursor(t *testing.T) {
	m := press(newModel(time.Now()), "r")
	if m.insert || m.replyThreadFocus {
		t.Fatal("reply must open focused in Normal mode")
	}
	cursor := m.View().Cursor
	if cursor == nil || cursor.Shape != tea.CursorBlock {
		t.Fatal("Normal mode must show a block cursor")
	}
	m = press(m, "hjkl")
	if m.compose.body != "" {
		t.Fatal("Normal navigation must not type")
	}
	m = press(m, "i日本語")
	cursor = m.View().Cursor
	left, _ := m.replyWidths()
	if cursor == nil || cursor.Shape != tea.CursorBar || cursor.X != left+7+6 || cursor.Y != 9 {
		t.Fatalf("Insert cursor must be a bar positioned after wide characters: %+v", cursor)
	}
	m = special(m, tea.KeyEscape)
	if m.insert || m.View().Cursor.Shape != tea.CursorBlock {
		t.Fatal("Esc must restore Normal block cursor")
	}
	for _, state := range []string{"help", "close", "command", "thread"} {
		n := m
		switch state {
		case "help":
			n = special(n, tea.KeyF1)
		case "close":
			n = control(n, 'q')
		case "command":
			n = press(n, ":")
		case "thread":
			n = window(n, "h")
		}
		if n.View().Cursor != nil {
			t.Fatalf("%s must hide draft cursor", state)
		}
	}
}

func TestReplyWindowNavigationPreservesDraft(t *testing.T) {
	m := newModel(time.Now())
	m.query = "Kyoto"
	m = press(m, "lriA reply to keep")
	id, body, cursor := m.current().ID, m.compose.body, m.bodyCursor
	m = window(m, "h") // Works from Insert, without typing h into the draft.
	if !m.replyThreadFocus || m.insert {
		t.Fatal("Ctrl+w h must focus the thread in Normal mode")
	}
	m = press(m, "G")
	if m.replyThreadScroll == 0 {
		t.Fatal("must be able to read to the oldest message")
	}
	scroll := m.replyThreadScroll
	m = press(m, "jixdd")
	next, _ := m.Update(tea.PasteMsg{Content: "do not insert"})
	m = next.(model)
	if m.compose.body != body || m.bodyCursor != cursor || m.replyThreadScroll != scroll {
		t.Fatal("thread navigation/edit keys/paste must preserve draft and clamp scroll")
	}
	m = window(m, "l")
	if m.replyThreadFocus || m.insert || m.bodyCursor != cursor || m.replyThreadScroll != scroll {
		t.Fatal("returning to reply must restore draft cursor in Normal, retaining thread scroll")
	}
	m = press(m, "i!")
	m = window(m, "h")
	m = press(m, "gg")
	if m.replyThreadScroll != 0 || m.compose.body != body+"!" {
		t.Fatal("thread gg must not affect reply")
	}
	m = special(m, tea.KeyEscape)
	if !m.editorClose {
		t.Fatal("Esc from thread must use the unsaved draft close flow")
	}
	m = press(m, "j")
	m = special(m, tea.KeyEnter)
	if m.compose != nil || !m.reading || m.current().ID != id || m.query != "Kyoto" {
		t.Fatal("Save & close from thread must restore original reading view")
	}
	m.folder, m.query, m.selected = "Drafts", "", 0
	m = press(m, "r")
	if m.replyThread() == nil || m.replyThread().ID != id || m.compose.body != body+"!" {
		t.Fatal("reopening a saved draft must show its full original thread")
	}
	content := strings.Join(strings.Fields(ansi.Strip(strings.Join(m.replyThreadLines(), "\n"))), " ")
	previous := -1
	for _, message := range m.replyThread().Messages {
		needle := strings.Join(strings.Fields(message.Body), " ")
		at := strings.Index(content, needle)
		if at <= previous {
			t.Fatal("full thread must include every body newest first")
		}
		previous = at
	}
}

func TestReplyResizeAndInputIsolation(t *testing.T) {
	m := press(newModel(time.Now()), "ri")
	next, _ := m.Update(tea.PasteMsg{Content: strings.Repeat("a long line with words 日本語 ", 100)})
	m = next.(model)
	body := m.compose.body
	for _, width := range []int{150, 96, 95, 48, 120} {
		next, _ = m.Update(tea.WindowSizeMsg{Width: width, Height: 24})
		m = next.(model)
		cursor := m.View().Cursor
		if cursor == nil || cursor.X < 7 || cursor.X >= width-1 || cursor.Y < 9 || cursor.Y >= 22 {
			t.Fatalf("width %d: draft cursor must remain visible after rewrap: %+v", width, cursor)
		}
		left, right := m.replyWidths()
		if width >= 96 && (left == 0 || right == 0) {
			t.Fatal("wide terminal must show both panes")
		}
		if width < 96 && (left != 0 || right != width) {
			t.Fatal("narrow terminal must show focused editor")
		}
		m = window(m, "h")
		left, right = m.replyWidths()
		if width < 96 && (left != width || right != 0) {
			t.Fatal("narrow terminal must show focused thread")
		}
		m = window(m, "l")
	}
	m = press(m, "i")
	m = control(m, 'w')
	next, _ = m.Update(tea.PasteMsg{Content: "ignored prefix paste"})
	m = next.(model)
	m = special(m, tea.KeyEscape)
	if m.replyWindowPending || !m.insert || m.compose.body != body {
		t.Fatal("Esc must cancel only window prefix; paste must not leak")
	}
	next, _ = m.Update(tea.MouseWheelMsg{X: 10, Y: 12, Button: tea.MouseWheelDown})
	m = next.(model)
	if m.replyThreadScroll == 0 || m.replyThreadFocus || !m.insert || m.compose.body != body {
		t.Fatal("wheel over thread must scroll independently while preserving edit focus")
	}
	next, _ = m.Update(tea.MouseClickMsg{X: 10, Y: 12, Button: tea.MouseLeft})
	m = next.(model)
	if !m.replyThreadFocus || m.insert {
		t.Fatal("click on thread must focus it in Normal")
	}
}
