package app

import (
	"fmt"
	"strings"
	"unicode"

	tea "charm.land/bubbletea/v2"
	lip "charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

type editSnapshot struct {
	body   string
	cursor int
}
type caret struct{ row, col int }

// Visual positions use terminal cell widths, so wrapped lines and wide text
// share the same geometry for rendering, vertical movement, and scrolling.
func editorLayout(body string, width int) ([]string, []caret) {
	lines := []string{""}
	runes := []rune(body)
	points := make([]caret, len(runes)+1)
	row, col := 0, 0
	for i, r := range runes {
		cw := ansi.StringWidth(string(r))
		// Keep ordinary words together; long unbroken words still wrap safely.
		if col > 0 && !unicode.IsSpace(r) && (i == 0 || unicode.IsSpace(runes[i-1])) {
			end := i
			for end < len(runes) && !unicode.IsSpace(runes[end]) {
				end++
			}
			wordWidth := ansi.StringWidth(string(runes[i:end]))
			if wordWidth <= width && col+wordWidth > width {
				lines = append(lines, "")
				row++
				col = 0
			}
		}
		if r != '\n' && cw > 0 && col+cw > width {
			lines = append(lines, "")
			row++
			col = 0
		}
		points[i] = caret{row, col}
		if r == '\n' {
			lines = append(lines, "")
			row++
			col = 0
		} else {
			lines[row] += string(r)
			col += cw
		}
	}
	points[len(points)-1] = caret{row, col}
	return lines, points
}

func (m model) editorWidth() int {
	_, w := m.replyWidths()
	if w == 0 {
		w = m.width
	}
	return max(8, w-10)
}
func (m model) editorVisible() int { return max(1, m.bodyHeight()-5) }
func (m model) editorPosition() caret {
	_, points := editorLayout(m.compose.body, m.editorWidth())
	return points[min(m.bodyCursor, len(points)-1)]
}
func (m *model) ensureCaret() {
	if m.compose == nil {
		return
	}
	m.bodyCursor = max(0, min(m.bodyCursor, len([]rune(m.compose.body))))
	p := m.editorPosition()
	m.composeScroll = max(0, min(m.composeScroll, p.row))
	if p.row >= m.composeScroll+m.editorVisible() {
		m.composeScroll = p.row - m.editorVisible() + 1
	}
}
func (m *model) rememberEdit() {
	m.undo = append(m.undo, editSnapshot{m.compose.body, m.bodyCursor})
	if len(m.undo) > 200 {
		m.undo = m.undo[len(m.undo)-200:]
	}
	m.redo = nil
	m.goalColumn = -1
}
func (m *model) insertText(s string) {
	// Pasted tabs and CRLF get predictable visible positions in the terminal.
	s = strings.ReplaceAll(strings.ReplaceAll(strings.ReplaceAll(s, "\r\n", "\n"), "\r", "\n"), "\t", "    ")
	s = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) && r != '\n' {
			return -1
		}
		return r
	}, s)
	if s == "" {
		return
	}
	m.rememberEdit()
	r := []rune(m.compose.body)
	at := min(m.bodyCursor, len(r))
	m.compose.body = string(r[:at]) + s + string(r[at:])
	m.bodyCursor = at + len([]rune(s))
	m.ensureCaret()
}
func (m *model) deleteRange(start, end int) {
	r := []rune(m.compose.body)
	start = max(0, start)
	end = min(len(r), end)
	if start >= end {
		return
	}
	m.rememberEdit()
	m.compose.body = string(r[:start]) + string(r[end:])
	m.bodyCursor = start
	m.ensureCaret()
}
func (m *model) undoEdit(redo bool) {
	from, to := &m.undo, &m.redo
	if redo {
		from, to = &m.redo, &m.undo
	}
	if len(*from) == 0 {
		return
	}
	*to = append(*to, editSnapshot{m.compose.body, m.bodyCursor})
	s := (*from)[len(*from)-1]
	*from = (*from)[:len(*from)-1]
	m.compose.body = s.body
	m.bodyCursor = s.cursor
	m.goalColumn = -1
	m.ensureCaret()
}
func (m model) lineBounds() (start, end int) {
	r := []rune(m.compose.body)
	start = m.bodyCursor
	end = start
	for start > 0 && r[start-1] != '\n' {
		start--
	}
	for end < len(r) && r[end] != '\n' {
		end++
	}
	return
}
func (m *model) moveVertical(delta int) {
	_, points := editorLayout(m.compose.body, m.editorWidth())
	p := points[m.bodyCursor]
	if m.goalColumn < 0 {
		m.goalColumn = p.col
	}
	target := max(0, min(p.row+delta, points[len(points)-1].row))
	best, distance := m.bodyCursor, 1<<30
	for i, p := range points {
		if p.row == target {
			d := p.col - m.goalColumn
			if d < 0 {
				d = -d
			}
			if d < distance {
				best = i
				distance = d
			}
		}
	}
	m.bodyCursor = best
	m.ensureCaret()
}
func wordClass(r rune) int {
	if unicode.IsSpace(r) {
		return 0
	}
	if unicode.IsLetter(r) || unicode.IsNumber(r) || r == '_' {
		return 1
	}
	return 2
}
func (m *model) moveWord(back bool) {
	r := []rune(m.compose.body)
	i := m.bodyCursor
	if back {
		if i > 0 {
			i--
		}
		for i > 0 && wordClass(r[i]) == 0 {
			i--
		}
		if len(r) > 0 {
			c := wordClass(r[i])
			for i > 0 && wordClass(r[i-1]) == c {
				i--
			}
		}
	} else {
		if i < len(r) {
			c := wordClass(r[i])
			for i < len(r) && wordClass(r[i]) == c {
				i++
			}
		}
		for i < len(r) && wordClass(r[i]) == 0 {
			i++
		}
	}
	m.bodyCursor = i
	m.goalColumn = -1
}

func (m model) handleCompose(key tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	k := key.String()
	if m.editorHelp {
		if k == "esc" || k == "f1" {
			m.editorHelp = false
		}
		return m, nil
	}
	if m.replyWindowPending {
		m.replyWindowPending = false
		switch k {
		case "h", "left", "ctrl+h":
			m.focusReplyPane(true)
		case "l", "right", "ctrl+l":
			m.focusReplyPane(false)
		}
		return m, nil
	}
	if k == "ctrl+w" {
		m.replyWindowPending = true
		m.editorPending = ""
		return m, nil
	}
	if k == "f1" {
		m.editorHelp = true
		return m, nil
	}
	if k == "ctrl+y" {
		m.sendDemo()
		return m, nil
	}
	if k == "ctrl+s" {
		m.saveDraft()
		return m, nil
	}
	if k == "esc" {
		m.editorPending = ""
		if m.insert {
			m.insert = false
			m.notice = "q closes the editor · i resumes typing"
		} else {
			m.requestEditorClose()
		}
		return m, nil
	}
	if m.replyThreadFocus {
		return m.handleReplyThread(k), nil
	}
	if k != "up" && k != "down" && k != "j" && k != "k" && k != "ctrl+d" && k != "ctrl+u" {
		m.goalColumn = -1
	}
	start, end := m.lineBounds()
	if !m.insert {
		if m.editorPending != "" {
			p := m.editorPending
			m.editorPending = ""
			if p+k == "gg" {
				m.bodyCursor = 0
			} else if p+k == "dd" {
				if end < len([]rune(m.compose.body)) {
					end++
				} else if start > 0 {
					start--
				}
				m.deleteRange(start, end)
			}
			m.ensureCaret()
			return m, nil
		}
		switch k {
		case "q":
			m.requestEditorClose()
		case "i":
			m.insert = true
		case "a":
			m.bodyCursor = min(m.bodyCursor+1, end)
			m.insert = true
		case "I":
			m.bodyCursor = start
			m.insert = true
		case "A":
			m.bodyCursor = end
			m.insert = true
		case "o":
			m.bodyCursor = end
			m.insertText("\n")
			m.insert = true
		case "O":
			m.bodyCursor = start
			m.insertText("\n")
			m.bodyCursor = start
			m.insert = true
		case "h", "left":
			m.bodyCursor = max(0, m.bodyCursor-1)
		case "l", "right":
			m.bodyCursor = min(len([]rune(m.compose.body)), m.bodyCursor+1)
		case "j", "down":
			m.moveVertical(1)
		case "k", "up":
			m.moveVertical(-1)
		case "w":
			m.moveWord(false)
		case "b":
			m.moveWord(true)
		case "0", "home":
			m.bodyCursor = start
		case "$", "end":
			m.bodyCursor = end
		case "g", "d":
			m.editorPending = k
		case "G":
			m.bodyCursor = len([]rune(m.compose.body))
		case "x", "delete":
			m.deleteRange(m.bodyCursor, m.bodyCursor+1)
		case "u":
			m.undoEdit(false)
		case "ctrl+r":
			m.undoEdit(true)
		case "ctrl+d", "pgdown":
			m.moveVertical(max(1, m.editorVisible()/2))
		case "ctrl+u", "pgup":
			m.moveVertical(-max(1, m.editorVisible()/2))
		case "?":
			m.editorHelp = true
		case ":":
			m.prompt = ":"
			m.input = ""
		}
	} else {
		switch k {
		case "backspace":
			m.deleteRange(m.bodyCursor-1, m.bodyCursor)
		case "delete":
			m.deleteRange(m.bodyCursor, m.bodyCursor+1)
		case "left":
			m.bodyCursor = max(0, m.bodyCursor-1)
		case "right":
			m.bodyCursor = min(len([]rune(m.compose.body)), m.bodyCursor+1)
		case "up":
			m.moveVertical(-1)
		case "down":
			m.moveVertical(1)
		case "home", "ctrl+a":
			m.bodyCursor = start
		case "end", "ctrl+e":
			m.bodyCursor = end
		case "enter":
			m.insertText("\n")
		case "tab":
			m.insertText("    ")
		default:
			if key.Text != "" {
				m.insertText(key.Text)
			}
		}
	}
	m.ensureCaret()
	return m, nil
}

func (m *model) editorCommand(command string) {
	if m.compose != nil && m.realAccount(m.compose.account) {
		if strings.HasPrefix(command, "to ") {
			m.compose.to = strings.TrimSpace(strings.TrimPrefix(command, "to "))
			m.notice = "Recipients updated · Ctrl+S save"
			return
		}
		if strings.HasPrefix(command, "subject ") {
			m.compose.subject = strings.TrimSpace(strings.TrimPrefix(command, "subject "))
			m.notice = "Subject updated · Ctrl+S save"
			return
		}
		if command == "retry-send" {
			m.sendLive(true)
			return
		}
	}
	switch command {
	case "q", "quit":
		m.requestEditorClose()
	case "q!":
		m.closeCompose()
		m.notice = "Editor closed. Unsaved edits discarded."
	case "w":
		m.saveDraft()
	case "wq", "x":
		if !m.saveDraft() {
			return
		}
		m.closeCompose()
		m.notice = "Draft kept in Drafts · r on this thread resumes it."
	case "send":
		m.sendDemo()
	case "help":
		m.editorHelp = true
	default:
		m.notice = "Editor commands: :w save · :wq save & close · :q close · :q! discard · :send"
	}
}
func (m *model) closeCompose() {
	m.compose = nil
	m.replyThreadFocus = false
	m.replyWindowPending = false
	m.replyThreadScroll = 0
	m.editorClose = false
	m.editorHelp = false
	m.replyWindowPending = false
	m.editorPending = ""
	m.prompt = ""
	m.input = ""
	m.undo = nil
	m.redo = nil
	list := m.folderThreads()
	for i, t := range list {
		if t.ID == m.editorReturnID {
			m.selected = i
			break
		}
	}
	if len(list) == 0 {
		m.reading = false
		m.focus = listPane
	}
	m.normalizeSelection()
}
func (m model) dirtyDraft() bool {
	return m.compose != nil && (m.compose.body != m.compose.savedBody || (m.realAccount(m.compose.account) && (m.compose.to != m.compose.savedTo || m.compose.subject != m.compose.savedSubject)))
}
func (m *model) requestEditorClose() {
	m.editorHelp = false
	m.replyWindowPending = false
	m.editorPending = ""
	m.prompt = ""
	m.input = ""
	if !m.dirtyDraft() {
		saved := m.compose.savedID != 0
		m.closeCompose()
		m.notice = "Editor closed."
		if saved {
			m.notice = "Draft kept in Drafts · r on this thread resumes it."
		}
		return
	}
	m.editorClose = true
	m.editorChoice = 0
}

func (m model) editorFooter() (string, string) {
	mode := "Normal"
	if m.insert {
		mode = "Insert"
	}
	if m.prompt != "" {
		mode = "Command"
	}
	state := "Unchanged"
	if m.realAccount(m.compose.account) {
		state = "Autosave on · local drafts"
		if m.live.sending {
			state = "Sending…"
		}
	}
	if m.compose.savedID != 0 {
		state = "Saved in Drafts"
	}
	if m.dirtyDraft() {
		state = "Unsaved changes"
	}
	p := m.editorPosition()
	paneName := "Reply"
	if m.replyThreadFocus {
		paneName = "Thread"
	}
	mode += " · " + paneName
	badge := lip.NewStyle().Foreground(ink).Background(cyan).Bold(true).Render(" " + mode + " ")
	position := fmt.Sprintf(" Ln %d, Col %d ", p.row+1, p.col+1)
	if m.replyThreadFocus {
		position = fmt.Sprintf(" Line %d/%d ", m.replyThreadScroll+1, max(1, len(m.replyThreadLines())))
	}
	status := onSurface(joinSides(badge+styled("  "+state, oceanText), styled(position, violet), m.width), statusPane)
	hint := " i ^S save ^Y send ^W h/l q close F1 help"
	if m.insert {
		hint = " Esc ^S save ^Y send ^W h/l ^Q close F1 help"
	} else if m.replyThreadFocus {
		hint = " j/k ^S save ^Y send ^W l q close F1 help"
	}
	if m.width >= 90 {
		if m.insert {
			hint = " Ctrl+Y send  Ctrl+S save  Ctrl+Q close  Esc normal  Ctrl+w h/l panes  F1 help"
		} else if m.replyThreadFocus {
			hint = " Ctrl+Y send  Ctrl+S save  Ctrl+w l reply  j/k scroll  gg/G jump  q close  F1 help"
		} else {
			hint = " Ctrl+Y send  Ctrl+S save  :wq save+close  q close  i insert  Ctrl+w h/l panes  F1 help"
		}
	}
	if m.replyWindowPending {
		hint = " Ctrl+w … h thread / l reply · Esc cancel"
	}
	if m.editorHelp {
		hint = " Esc / F1 back to editor   Ctrl+Q close"
	}
	if m.prompt != "" {
		hint = " " + m.prompt + m.input + "▏  · Enter run · Esc cancel"
	}
	return status, styled(hint, muted)
}

func (m model) composeView() string {
	if m.editorHelp {
		return m.editorHelpView()
	}
	left, right := m.replyWidths()
	var panes []string
	if left > 0 {
		panes = append(panes, m.replyThreadView(left, right > 0))
	}
	if right > 0 {
		panes = append(panes, m.replyEditorView(right))
	}
	return lip.JoinHorizontal(lip.Top, panes...)
}
func (m model) editorOffset() int {
	p := m.editorPosition()
	offset := max(0, min(m.composeScroll, p.row))
	if p.row >= offset+m.editorVisible() {
		offset = p.row - m.editorVisible() + 1
	}
	return offset
}
func (m model) replyEditorView(width int) string {
	d := m.compose
	label := "Reply"
	if m.realAccount(d.account) && d.replyID == "" {
		label = "New message"
	}
	if !m.replyThreadFocus {
		label += " ●"
	}
	to, subject := d.to, d.subject
	if m.realAccount(d.account) {
		if to == "" {
			to = "(use :to address@example.com)"
		}
		if subject == "" {
			subject = "(use :subject Your subject)"
		}
	}
	lines := []string{bold(label, violet), styled("From: "+m.accounts[d.account].Address, cyan), styled("To:   "+safeMailText(to), muted), bold(safeMailText(subject), text), gradient(strings.Repeat("─", max(1, width-3)), 0)}
	wrapped, _ := editorLayout(d.body, m.editorWidth())
	offset := m.editorOffset()
	for row := offset; row < min(len(wrapped), offset+m.editorVisible()); row++ {
		lines = append(lines, styled(fit(fmt.Sprintf("%3d", row+1), 3)+" │ ", dim)+styled(wrapped[row], readerText))
	}
	return paneLines(lines, width, m.bodyHeight(), false, violetPane)
}
func (m model) editorHelpView() string {
	lines := []string{bold("Writing mail · Keymap", cyan), styled("Esc/F1 returns · Ctrl+Q closes the editor", oceanText), gradient(strings.Repeat("─", m.width-3), 0)}
	for _, s := range []string{
		"Ctrl+y / :send: send (real accounts deliver mail)",
		"Ctrl+s / :w save · :wq keep in Drafts & close",
		"r on a conversation resumes its saved draft",
		"Opens in Normal · block cursor · i to insert",
		"Insert: bar cursor · type, paste, arrows, Home/End",
		"Ctrl+w then h/l: thread left / reply right",
		"Thread: j/k, gg/G, Ctrl+d/u · separate scroll",
		"Esc: Insert → Normal → close editor",
		"Normal: hjkl, w/b, 0/$, gg/G, Ctrl+d/u",
		"i/a/I/A: type · o/O: new line · x/dd: delete",
		"u: undo · Ctrl+r: redo · q: close editor",
		"Ctrl+s / :w save · :wq save & close",
		":to address · :subject text: edit message headers",
		":q close · :q! discard unsaved edits",
	} {
		lines = append(lines, styled(s, text))
	}
	return paneLines(lines, m.width, m.bodyHeight(), false, bluePane)
}
