package app

import (
	"strings"

	tea "charm.land/bubbletea/v2"
	lip "charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

func (m *model) requestQuit() {
	if m.reading && m.compose == nil {
		m.reading = false
		m.focus = listPane
		m.scroll = 0
		return
	}
	m.quitOpen = true
	m.quitSelected = false
	m.loading = false
}

func (m model) handleQuit(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyPressMsg:
		switch msg.String() {
		case "esc", "ctrl+c":
			m.quitOpen = false
		case "tab", "shift+tab":
			m.quitSelected = !m.quitSelected
		case "h", "k", "left", "up":
			m.quitSelected = false
		case "l", "j", "right", "down":
			m.quitSelected = true
		case "enter":
			if m.quitSelected {
				return m, tea.Quit
			}
			m.quitOpen = false
		}
	case tea.MouseClickMsg:
		if msg.Button != tea.MouseLeft {
			break
		}
		x, y, _, _ := m.quitGeometry()
		bx, by := m.quitButtons()
		if msg.Y != y+by {
			break
		}
		if msg.X >= x+bx && msg.X < x+bx+10 {
			m.quitOpen = false
		}
		if msg.X >= x+bx+12 && msg.X < x+bx+22 {
			return m, tea.Quit
		}
	}
	return m, nil
}

func (m model) quitGeometry() (x, y, w, h int) {
	w = max(1, min(56, m.width-4))
	h = max(1, min(11, m.height))
	return max(0, (m.width-w)/2), max(0, (m.height-h)/2), w, h
}

func (m model) quitButtons() (x, y int) {
	_, _, w, h := m.quitGeometry()
	return max(1, (w-22)/2), max(1, h-5)
}

func (m model) quitView() []string {
	_, _, w, h := m.quitGeometry()
	inner := max(0, w-2)
	rows := make([]string, h)
	for i := range rows {
		rows[i] = styled("│", violet) + strings.Repeat(" ", inner) + styled("│", violet)
	}
	put := func(y int, s string) {
		if y > 0 && y < h-1 {
			rows[y] = styled("│", violet) + fit(s, inner) + styled("│", violet)
		}
	}
	put(1, center(bold("Quit "+appName+"?", cyan), inner))
	message := "Your sample changes reset when you leave."
	if m.compose != nil {
		message = "Your open reply will be discarded."
	}
	put(3, center(styled(message, readerText), inner))
	bx, by := m.quitButtons()
	button := func(label string, selected bool) string {
		st := lip.NewStyle().Foreground(cyan).Background(bluePane)
		if selected {
			st = st.Foreground(ink).Background(cyan).Bold(true)
		}
		return st.Render(fit(label, 10))
	}
	put(by, strings.Repeat(" ", max(0, bx-1))+button("  Cancel  ", !m.quitSelected)+"  "+button("   Quit   ", m.quitSelected))
	put(h-3, center(styled("h/l or Tab select · Enter confirm", muted), inner))
	put(h-2, center(styled("Esc to keep reading", oceanText), inner))
	rows[0] = gradient("╭"+strings.Repeat("─", inner)+"╮", 0)
	if h > 1 {
		rows[h-1] = gradient("╰"+strings.Repeat("─", inner)+"╯", 0)
	}
	for i := range rows {
		rows[i] = onSurface(fit(rows[i], w), violetPane)
	}
	return rows
}

func (m model) overlayQuit(lines []string) []string {
	x, y, w, _ := m.quitGeometry()
	for i, row := range m.quitView() {
		if y+i >= len(lines) {
			break
		}
		under := lines[y+i]
		lines[y+i] = ansi.Cut(under, 0, x) + row + ansi.Cut(under, x+w, m.width)
	}
	return lines
}
