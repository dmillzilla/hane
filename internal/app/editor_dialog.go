package app

import (
	"strings"

	tea "charm.land/bubbletea/v2"
	lip "charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

func (m model) editorDialogGeometry() (x, y, w, h int) {
	w = max(1, min(54, m.width-4))
	h = max(1, min(12, m.height))
	return max(0, (m.width-w)/2), max(0, (m.height-h)/2), w, h
}
func (m *model) chooseEditorClose() {
	switch m.editorChoice {
	case 0:
		m.editorClose = false
	case 1:
		if !m.saveDraft() {
			return
		}
		m.closeCompose()
		m.notice = "Draft kept in Drafts · r on this thread resumes it."
	case 2:
		m.closeCompose()
		m.notice = "Editor closed. Unsaved edits discarded."
	}
}
func (m model) handleEditorClose(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyPressMsg:
		switch msg.String() {
		case "esc", "ctrl+c", "ctrl+q":
			m.editorClose = false
		case "j", "l", "down", "right", "tab":
			m.editorChoice = (m.editorChoice + 1) % 3
		case "k", "h", "up", "left", "shift+tab":
			m.editorChoice = (m.editorChoice + 2) % 3
		case "enter":
			m.chooseEditorClose()
		}
	case tea.MouseClickMsg:
		x, y, w, h := m.editorDialogGeometry()
		choice := msg.Y - y - max(3, h-7)
		if msg.Button == tea.MouseLeft && msg.X >= x+2 && msg.X < x+w-2 && choice >= 0 && choice < 3 {
			m.editorChoice = choice
			m.chooseEditorClose()
		}
	}
	return m, nil
}
func (m model) overlayEditorClose(lines []string) []string {
	x, y, w, h := m.editorDialogGeometry()
	inner := max(0, w-2)
	rows := make([]string, h)
	for i := range rows {
		rows[i] = styled("│", violet) + strings.Repeat(" ", inner) + styled("│", violet)
	}
	put := func(i int, s string) {
		if i > 0 && i < h-1 {
			rows[i] = styled("│", violet) + fit(s, inner) + styled("│", violet)
		}
	}
	put(1, center(bold("Close this reply?", cyan), inner))
	put(3, center(styled("You have unsaved changes.", readerText), inner))
	for i, label := range []string{"Keep editing", "Save to Drafts & close", "Discard changes"} {
		prefix := "  "
		st := lip.NewStyle().Foreground(cyan).Background(bluePane)
		if i == m.editorChoice {
			prefix = "› "
			st = st.Foreground(ink).Background(cyan).Bold(true)
		}
		put(max(3, h-7)+i, " "+st.Render(fit(prefix+label, max(1, inner-2))))
	}
	put(h-3, center(styled("j/k or Tab select · Enter confirm", muted), inner))
	put(h-2, center(styled("Esc keeps editing", oceanText), inner))
	rows[0] = gradient("╭"+strings.Repeat("─", inner)+"╮", 0)
	if h > 1 {
		rows[h-1] = gradient("╰"+strings.Repeat("─", inner)+"╯", 0)
	}
	for i, row := range rows {
		if y+i >= len(lines) {
			break
		}
		under := lines[y+i]
		lines[y+i] = ansi.Cut(under, 0, x) + onSurface(fit(row, w), violetPane) + ansi.Cut(under, x+w, m.width)
	}
	return lines
}
