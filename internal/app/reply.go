package app

import (
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"
)

// Narrow terminals show the focused pane; the same window keys still apply.
func (m model) replyWidths() (left, right int) {
	if m.compose != nil && m.realAccount(m.compose.account) && m.compose.replyID == "" {
		return 0, m.width
	}
	if m.width >= 96 {
		return m.width / 2, m.width - m.width/2
	}
	if m.replyThreadFocus {
		return m.width, 0
	}
	return 0, m.width
}

func (m model) replyThread() *Thread {
	if m.compose != nil {
		for i := range m.threads {
			t := &m.threads[i]
			if t.ID == m.compose.threadID && t.Account == m.compose.account {
				return t
			}
		}
	}
	return nil
}

func (m model) replyThreadLines() []string {
	left, _ := m.replyWidths()
	if left == 0 {
		left = m.width
	}
	return threadLines(m.replyThread(), max(8, left-4))
}

func (m model) replyThreadVisible() int { return max(1, m.bodyHeight()-4) }

func (m *model) clampReplyScroll() {
	if m.compose == nil {
		return
	}
	m.replyThreadScroll = max(0, min(m.replyThreadScroll, len(m.replyThreadLines())-m.replyThreadVisible()))
}

func (m *model) focusReplyPane(thread bool) {
	if thread && m.compose != nil && m.realAccount(m.compose.account) && m.compose.replyID == "" {
		m.notice = "This is a new message; there is no previous thread."
		return
	}
	if m.replyThreadFocus != thread {
		m.insert = false
	}
	m.replyThreadFocus = thread
	m.replyWindowPending = false
	m.editorPending = ""
	m.ensureCaret()
	m.clampReplyScroll()
}

func (m model) handleReplyThread(k string) model {
	if m.editorPending != "" {
		pending := m.editorPending
		m.editorPending = ""
		if pending+k == "gg" {
			m.replyThreadScroll = 0
		}
		return m
	}
	switch k {
	case "j", "down":
		m.replyThreadScroll++
	case "k", "up":
		m.replyThreadScroll--
	case "g":
		m.editorPending = "g"
	case "G":
		m.replyThreadScroll = len(m.replyThreadLines())
	case "ctrl+d", "pgdown":
		m.replyThreadScroll += max(1, m.replyThreadVisible()/2)
	case "ctrl+u", "pgup":
		m.replyThreadScroll -= max(1, m.replyThreadVisible()/2)
	case "q":
		m.requestEditorClose()
	case "?":
		m.editorHelp = true
	case ":":
		m.prompt = ":"
		m.input = ""
	}
	m.clampReplyScroll()
	return m
}

func (m model) replyThreadView(width int, separator bool) string {
	label := "Thread"
	if m.replyThreadFocus {
		label += " ●"
	}
	t := m.replyThread()
	if t == nil {
		return paneLines([]string{bold(label, teal), styled("Original thread unavailable.", muted), styled("Your draft is still editable on the right.", muted)}, width, m.bodyHeight(), separator, bluePane)
	}
	lines := []string{joinSides(bold(label, teal), styled(threadLabel(t), violet), width-3), bold(t.Subject, text), gradient(strings.Repeat("─", max(1, width-3)), 0)}
	content := m.replyThreadLines()
	limit := max(0, len(content)-m.replyThreadVisible())
	offset := max(0, min(m.replyThreadScroll, limit))
	lines = append(lines, content[offset:min(len(content), offset+m.replyThreadVisible())]...)
	for len(lines) < m.bodyHeight()-1 {
		lines = append(lines, "")
	}
	percent := 100
	if limit > 0 {
		percent = offset * 100 / limit
	}
	lines = append(lines, styled(fmt.Sprintf("Newest first · j/k scroll · %d%%", percent), muted))
	return paneLines(lines, width, m.bodyHeight(), separator, bluePane)
}

// Use the terminal cursor so Normal and Insert follow Neovim's block/bar
// distinction. Hide it when the draft is not accepting navigation or text.
func (m model) replyCursor() *tea.Cursor {
	if m.compose == nil || m.replyThreadFocus || m.editorHelp || m.editorClose || m.quitOpen || m.prompt != "" || m.help || m.loading || m.width < 48 || m.height < 16 {
		return nil
	}
	left, _ := m.replyWidths()
	p := m.editorPosition()
	x, y := left+7+p.col, 9+p.row-m.editorOffset()
	if x >= m.width-1 || y >= m.height-2 {
		return nil
	}
	cursor := tea.NewCursor(x, y)
	cursor.Color = cyan
	cursor.Shape = tea.CursorBlock
	cursor.Blink = false
	if m.insert {
		cursor.Shape = tea.CursorBar
		cursor.Blink = true
	}
	return cursor
}
