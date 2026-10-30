package main

import (
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
)

func control(m model, r rune) model {
	next, _ := m.Update(tea.KeyPressMsg{Code: r, Mod: tea.ModCtrl})
	return next.(model)
}
func editorCmd(m model, s string) model { return special(press(m, ":"+s), tea.KeyEnter) }

func TestEditorExitRoutes(t *testing.T) {
	for _, route := range []string{"double escape", "q", "ctrl+q", "ctrl+c", ":q", ":quit"} {
		t.Run(route, func(t *testing.T) {
			m := press(newModel(time.Now()), "jjlriKeep my reply")
			id := m.current().ID
			switch route {
			case "double escape":
				m = special(special(m, tea.KeyEscape), tea.KeyEscape)
			case "q":
				m = press(special(m, tea.KeyEscape), "q")
			case "ctrl+q":
				m = control(m, 'q')
			case "ctrl+c":
				m = control(m, 'c')
			default:
				m = editorCmd(special(m, tea.KeyEscape), strings.TrimPrefix(route, ":"))
			}
			if !m.editorClose || m.quitOpen || m.compose == nil || m.editorChoice != 0 {
				t.Fatal("exit must ask about unsaved reply with Keep editing selected")
			}
			m = special(m, tea.KeyEnter)
			if m.editorClose || m.compose.body != "Keep my reply" {
				t.Fatal("default action must keep the reply")
			}
			m = control(m, 'q')
			m = press(m, "j")
			m = special(m, tea.KeyEnter)
			if m.compose != nil || m.editorClose || m.quitOpen || m.current().ID != id || !m.reading || m.count(0, "Drafts") != 1 {
				t.Fatal("Save & close must save once and restore the original thread")
			}
		})
	}
}

func TestSaveKeepsEditorAndCloseDoesNotDuplicate(t *testing.T) {
	m := press(newModel(time.Now()), "riOne version")
	m = control(m, 's')
	if m.compose == nil || m.dirtyDraft() || m.count(0, "Drafts") != 1 {
		t.Fatal("Ctrl+S must save and keep editing")
	}
	m = press(m, " with edits")
	m = special(m, tea.KeyEscape)
	m = editorCmd(m, "w")
	if m.compose == nil || m.dirtyDraft() || m.count(0, "Drafts") != 1 {
		t.Fatal(":w must update the same draft and keep it open")
	}
	m = press(m, "q")
	if m.compose != nil || m.editorClose || m.quitOpen {
		t.Fatal("unchanged saved draft should close immediately")
	}
	m.folder = "Drafts"
	m.selected = 0
	m = press(m, "ri changed again")
	m = control(m, 'q')
	m = press(m, "jj")
	m = special(m, tea.KeyEnter)
	if m.compose != nil || m.current().newest().Body != "One version with edits" {
		t.Fatal("Discard must retain the last saved draft")
	}
}

func TestEditorFooterWinsOverSearchAndFits(t *testing.T) {
	for _, width := range []int{48, 80, 90, 100, 120} {
		m := newModel(time.Now())
		m.width = width
		m.query = "Kyoto"
		m = press(m, "ri")
		for _, normal := range []bool{false, true} {
			if normal {
				m = special(m, tea.KeyEscape)
			}
			lines := strings.Split(ansi.Strip(m.View().Content), "\n")
			footer := lines[len(lines)-1]
			for _, word := range []string{"close", "F1"} {
				if !strings.Contains(footer, word) {
					t.Fatalf("width %d: missing %q in footer %q", width, word, footer)
				}
			}
			if strings.Contains(footer, "Kyoto") {
				t.Fatal("search replaced editor footer")
			}
		}
		m = special(m, tea.KeyEscape)
		if m.compose != nil || m.editorClose || m.query != "Kyoto" {
			t.Fatal("closing empty editor must preserve inbox search")
		}
	}
}

func TestEditorCursorMovesThroughWrappedUnicode(t *testing.T) {
	lines, _ := editorLayout("a notebook and my camera", 20)
	if len(lines) != 2 || lines[1] != "camera" {
		t.Fatal("soft wrap should preserve complete words")
	}
	m := newModel(time.Now())
	m.width = 48
	m = press(m, "ri")
	next, _ := m.Update(tea.PasteMsg{Content: "日本語のメールです。Hello world, here is a long wrapped line.\nshort\nA final line"})
	m = next.(model)
	m = special(m, tea.KeyEscape)
	m = press(m, "gg")
	m = press(m, "j")
	first := m.editorPosition()
	if first.row != 1 {
		t.Fatal("j should move by a displayed row")
	}
	m = press(m, "k")
	if m.bodyCursor != 0 {
		t.Fatal("k should return to first row")
	}
	m = press(m, "G")
	end := m.bodyCursor
	m = press(m, "i")
	m = special(m, tea.KeyUp)
	if m.bodyCursor >= end {
		t.Fatal("Insert-mode Up should move the cursor")
	}
	m = special(m, tea.KeyDown)
	if m.bodyCursor != end {
		t.Fatal("vertical movement should retain desired column across short lines")
	}
}

func TestEditorDeleteUndoRedoAndWords(t *testing.T) {
	m := press(newModel(time.Now()), "rihello world\nsecond line")
	m = special(m, tea.KeyEscape)
	m = press(m, "ggw")
	if m.bodyCursor != 6 {
		t.Fatal("w should jump to the next word")
	}
	m = press(m, "b")
	if m.bodyCursor != 0 {
		t.Fatal("b should return to the first word")
	}
	m = press(m, "dd")
	if m.compose.body != "second line" {
		t.Fatal("dd should remove current logical line")
	}
	m = press(m, "u")
	if m.compose.body != "hello world\nsecond line" {
		t.Fatal("undo should restore deleted line")
	}
	m = control(m, 'r')
	if m.compose.body != "second line" {
		t.Fatal("redo should reapply deletion")
	}
	m = press(m, "ggx")
	if m.compose.body != "econd line" {
		t.Fatal("x should delete under cursor")
	}
}

func TestEditorHelpAndModalBlockEdits(t *testing.T) {
	m := press(newModel(time.Now()), "ribody")
	m = special(m, tea.KeyF1)
	next, _ := m.Update(tea.PasteMsg{Content: "changed"})
	m = next.(model)
	m = press(m, "jtest")
	if m.compose.body != "body" {
		t.Fatal("help must block typing and paste")
	}
	m = special(m, tea.KeyEscape)
	m = control(m, 'q')
	next, _ = m.Update(tea.PasteMsg{Content: "changed"})
	m = next.(model)
	x, y, _, h := m.editorDialogGeometry()
	next, _ = m.Update(tea.MouseClickMsg{X: x + 5, Y: y + max(3, h-7) + 1, Button: tea.MouseLeft})
	m = next.(model)
	if m.compose != nil || m.count(0, "Drafts") != 1 {
		t.Fatal("Save & close mouse action should work")
	}
}
