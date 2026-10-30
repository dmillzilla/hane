package main

import (
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
)

func TestQuitRequiresExplicitSelection(t *testing.T) {
	m := newModel(time.Now())
	next, cmd := m.Update(tea.KeyPressMsg{Code: 'q', Text: "q"})
	m = next.(model)
	if cmd != nil || !m.quitOpen || m.quitSelected {
		t.Fatal("quit should open with Cancel selected")
	}
	m = special(m, tea.KeyEnter)
	if m.quitOpen {
		t.Fatal("Enter on default Cancel should resume the app")
	}
	m = press(m, "ql")
	next, cmd = m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("confirmed Quit should exit")
	}
	if _, ok := cmd().(tea.QuitMsg); !ok {
		t.Fatal("expected quit command")
	}
}

func TestQuitModalPreservesReplyAndBlocksInput(t *testing.T) {
	m := press(newModel(time.Now()), "riKeep this reply")
	next, _ := m.Update(tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl})
	m = next.(model)
	if !m.editorClose || m.quitOpen {
		t.Fatal("Ctrl+C should request closing the editor, not the app")
	}
	next, _ = m.Update(tea.PasteMsg{Content: "unwanted text"})
	m = next.(model)
	next, _ = m.Update(tea.MouseWheelMsg{Button: tea.MouseWheelDown})
	m = next.(model)
	m = press(m, "typing")
	m = special(m, tea.KeyEscape)
	if m.editorClose || m.quitOpen || m.compose == nil || m.compose.body != "Keep this reply" || !m.insert {
		t.Fatal("cancel should preserve the exact editing state")
	}
}

func TestQuitCommandRoutes(t *testing.T) {
	for _, command := range []string{":q", ":quit", ":q!"} {
		m := special(press(newModel(time.Now()), command), tea.KeyEnter)
		if !m.quitOpen || m.prompt != "" {
			t.Fatalf("%s should show quit confirmation", command)
		}
	}
}

func TestQuitClosesThreadBeforeConfirmingExit(t *testing.T) {
	routes := map[string]func(model) model{
		"q": func(m model) model { return press(m, "q") },
		"Ctrl+C": func(m model) model {
			next, _ := m.Update(tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl})
			return next.(model)
		},
	}
	for _, command := range []string{":q", ":quit", ":q!"} {
		routes[command] = func(m model) model { return special(press(m, command), tea.KeyEnter) }
	}
	for name, quit := range routes {
		t.Run(name, func(t *testing.T) {
			m := press(newModel(time.Now()), "jjlG")
			selectedID := m.current().ID
			m = quit(m)
			if m.reading || m.quitOpen || m.focus != listPane || m.current().ID != selectedID {
				t.Fatal("first quit should return to the selected inbox thread without a popup")
			}
			m = quit(m)
			if !m.quitOpen || m.quitSelected {
				t.Fatal("second quit should open confirmation with Cancel selected")
			}
			m = special(m, tea.KeyEscape)
			if m.quitOpen || m.reading || m.current().ID != selectedID {
				t.Fatal("cancelling quit should leave the inbox selection intact")
			}
		})
	}
}

func TestQuitMouseButtons(t *testing.T) {
	for _, quit := range []bool{false, true} {
		m := newModel(time.Now())
		m.requestQuit()
		x, y, _, _ := m.quitGeometry()
		bx, by := m.quitButtons()
		offset := 0
		if quit {
			offset = 12
		}
		next, cmd := m.Update(tea.MouseClickMsg{X: x + bx + offset + 2, Y: y + by, Button: tea.MouseLeft})
		if quit {
			if cmd == nil {
				t.Fatal("Quit button should exit")
			}
		} else if next.(model).quitOpen || cmd != nil {
			t.Fatal("Cancel button should resume the app")
		}
	}
}
