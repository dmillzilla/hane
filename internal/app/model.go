package app

import (
	"context"
	"fmt"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
)

type pane int

const (
	listPane pane = iota
	treePane
	threadPane
)

type tickMsg time.Time
type treeRow struct {
	account int
	folder  string
}
type draft struct {
	replyID               string
	references            []string
	savedTo, savedSubject string
	account, threadID     int
	savedID               int
	to, subject, body     string
	savedBody             string
}
type model struct {
	live                                                 *liveState
	accounts                                             []Account
	threads                                              []Thread
	now                                                  time.Time
	width, height                                        int
	account                                              int
	folder, query                                        string
	selected, listOffset, treeCursor, treeOffset, scroll int
	expanded                                             []bool
	treeOpen, reading, loading, help                     bool
	focus                                                pane
	frame                                                int
	prefix, prompt, input, notice                        string
	compose                                              *draft
	insert                                               bool
	bodyCursor                                           int
	composeScroll                                        int
	quitOpen, quitSelected                               bool
	deletion                                             *deleteDialog
	editorClose, editorHelp                              bool
	editorChoice, editorReturnID, goalColumn             int
	editorPending                                        string
	undo, redo                                           []editSnapshot
	replyThreadFocus, replyWindowPending                 bool
	replyThreadScroll                                    int
	setup                                                *accountSetup
	setupID                                              uint64
	configPath, configError                              string
	credentials                                          credentialStore
	background                                           context.Context
}

func newModel(now time.Time) model {
	a, t := seed(now)
	return model{accounts: a, threads: t, now: now, width: 120, height: 36, folder: "Inbox", expanded: []bool{true, true}, treeOpen: true, treeCursor: 1, notice: "Welcome aboard. Press ? for the keymap."}
}
func tick() tea.Cmd {
	return tea.Tick(70*time.Millisecond, func(t time.Time) tea.Msg { return tickMsg(t) })
}
func (m model) Init() tea.Cmd {
	if m.live != nil {
		for i := range m.accounts {
			m.queueSync(i, syncRequest{Folder: inboxName(m.accounts[i])})
		}
		cmds := append([]tea.Cmd{refreshMailTick()}, m.live.pending...)
		m.live.pending = nil
		if m.loading {
			cmds = append(cmds, tick())
		}
		return tea.Batch(cmds...)
	}
	if m.loading {
		return tea.Batch(tick(), m.reconnectAccounts())
	}
	return m.reconnectAccounts()
}
func (m model) rows() []treeRow {
	var rows []treeRow
	for a, account := range m.accounts {
		rows = append(rows, treeRow{a, ""})
		if m.expanded[a] {
			for _, f := range account.Folders {
				rows = append(rows, treeRow{a, f})
			}
		}
	}
	return append(rows, treeRow{-1, ""})
}
func (m model) current() *Thread {
	list := m.folderThreads()
	if len(list) == 0 {
		return nil
	}
	return list[min(m.selected, len(list)-1)]
}
func (m *model) normalizeSelection() {
	m.selected = max(0, min(m.selected, len(m.folderThreads())-1))
	visible := max(1, (m.bodyHeight()-3)/6)
	m.listOffset = max(0, min(m.listOffset, m.selected))
	if m.selected >= m.listOffset+visible {
		m.listOffset = m.selected - visible + 1
	}
	m.treeCursor = max(0, min(m.treeCursor, len(m.rows())-1))
	rowsVisible := max(1, m.bodyHeight()-5)
	m.treeOffset = max(0, min(m.treeOffset, m.treeCursor))
	if m.treeCursor >= m.treeOffset+rowsVisible {
		m.treeOffset = m.treeCursor - rowsVisible + 1
	}
	m.clampScroll()
}
func (m *model) openThread() {
	if t := m.current(); t != nil {
		if t.QueueID != "" {
			m.reading, m.focus, m.scroll = true, threadPane, 0
			return
		}
		if isDraftThread(t) {
			m.startReply()
			return
		}
		if t.Remote {
			m.markLiveThread(t, "\\Seen", true)
			m.expandThread(t)
		} else {
			t.Unread = false
		}
		m.reading = true
		m.focus = threadPane
		m.scroll = 0
	}
}
func (m *model) toggleTree() {
	tw, _, _ := m.layout()
	m.treeOpen = tw == 0
	if m.treeOpen {
		m.focus = treePane
	} else if m.reading {
		m.focus = threadPane
	} else {
		m.focus = listPane
	}
}
func (m *model) revealTree() {
	m.treeOpen = true
	m.expanded[m.account] = true
	m.focus = treePane
	for i, r := range m.rows() {
		if r.account == m.account && r.folder == m.folder {
			m.treeCursor = i
			break
		}
	}
}
func (m *model) chooseFolder() {
	r := m.rows()[m.treeCursor]
	if r.account < 0 {
		m.openAccountSetup(-1)
		return
	}
	if r.folder == "" {
		m.expanded[r.account] = !m.expanded[r.account]
		return
	}
	m.account = r.account
	m.folder = r.folder
	m.query = ""
	m.selected = 0
	m.listOffset = 0
	m.scroll = 0
	m.reading = false
	m.focus = listPane
}
func (m *model) move(delta int) {
	switch m.focus {
	case treePane:
		m.treeCursor += delta
	case listPane:
		m.selected += delta
	case threadPane:
		m.scroll += delta
	}
	m.normalizeSelection()
}

func (m model) updateUI(msg tea.Msg) (tea.Model, tea.Cmd) {
	if result, ok := msg.(passwordSetupDone); ok {
		return m.finishPasswordSetup(result)
	}
	if result, ok := msg.(reconnectMsg); ok {
		return m.finishReconnect(result)
	}
	if result, ok := msg.(accountCheckMsg); ok {
		return m.finishAccountCheck(result)
	}
	if m.setup != nil {
		return m.handleAccountSetup(msg)
	}
	if m.editorClose {
		if _, resizing := msg.(tea.WindowSizeMsg); !resizing {
			return m.handleEditorClose(msg)
		}
	}
	if m.quitOpen {
		if _, resizing := msg.(tea.WindowSizeMsg); !resizing {
			return m.handleQuit(msg)
		}
	}
	if m.deletion != nil {
		if _, resizing := msg.(tea.WindowSizeMsg); !resizing {
			return m.handleDelete(msg)
		}
	}
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		m.normalizeSelection()
		m.ensureCaret()
		m.clampReplyScroll()
	case tickMsg:
		if m.loading {
			m.frame++
			if m.frame >= 20 {
				m.loading = false
			} else {
				return m, tick()
			}
		}
	case tea.PasteMsg:
		if m.editorHelp {
			return m, nil
		}
		if m.prompt != "" {
			m.input += strings.ReplaceAll(msg.Content, "\n", " ")
		} else if m.compose != nil && m.insert && !m.replyThreadFocus && !m.replyWindowPending {
			m.insertText(msg.Content)
		}
	case tea.MouseClickMsg:
		if m.loading {
			m.loading = false
			return m, nil
		}
		if m.compose != nil {
			if !m.editorHelp && m.prompt == "" && msg.Button == tea.MouseLeft && msg.Y >= 4 && msg.Y < m.height-2 {
				left, right := m.replyWidths()
				if left > 0 && right > 0 {
					m.focusReplyPane(msg.X < left)
				}
			}
			return m, nil
		}
		if m.help || m.prompt != "" {
			return m, nil
		}
		if msg.Button == tea.MouseLeft {
			m.click(msg.X, msg.Y)
		}
	case tea.MouseWheelMsg:
		if m.compose != nil {
			left, _ := m.replyWidths()
			if !m.editorHelp && m.prompt == "" && left > 0 && msg.X < left && msg.Y >= 4 && msg.Y < m.height-2 {
				delta := 3
				if msg.Button == tea.MouseWheelUp {
					delta = -3
				}
				m.replyThreadScroll += delta
				m.clampReplyScroll()
			}
			return m, nil
		}
		if m.loading || m.help || m.prompt != "" {
			return m, nil
		}
		delta := 3
		if msg.Button == tea.MouseWheelUp {
			delta = -3
		}
		m.move(delta)
	case tea.KeyPressMsg:
		key := msg.String()
		if key == "ctrl+c" {
			if m.compose != nil {
				m.requestEditorClose()
				return m, nil
			}
			m.requestQuit()
			return m, nil
		}
		if m.loading {
			m.loading = false
			return m, nil
		}
		if m.help {
			m.help = false
			return m, nil
		}
		if m.compose != nil && key == "ctrl+q" {
			m.requestEditorClose()
			return m, nil
		}
		if m.prompt != "" {
			return m.handlePrompt(msg)
		}
		if m.compose != nil {
			return m.handleCompose(msg)
		}
		if m.prefix != "" {
			p := m.prefix
			m.prefix = ""
			switch p + key {
			case "dd":
				m.requestDelete()
			case "\\n", " n":
				m.prefix = "\\n"
			case "\\nt":
				m.toggleTree()
			case "\\nf":
				m.revealTree()
			case "gg":
				if m.focus == threadPane {
					m.scroll = 0
				} else if m.focus == treePane {
					m.treeCursor = 0
				} else {
					m.selected = 0
				}
			case "ctrl+wh":
				if m.focus == threadPane {
					m.focus = listPane
				} else {
					m.revealTree()
				}
			case "ctrl+wl":
				if m.focus == treePane {
					m.focus = listPane
				} else if m.reading {
					m.focus = threadPane
				} else {
					m.openThread()
				}
			}
			m.normalizeSelection()
			return m, nil
		}
		switch key {
		case "Q":
			m.openSyncQueue()
		case "R", "f5":
			m.syncCurrent(false)
		case "L":
			m.syncCurrent(true)
		case "c":
			m.startLiveCompose()
		case "B":
			return m.launchPasswordSetup()
		case "A":
			m.openAccountSetup(-1)
		case "e":
			if m.focus == treePane {
				r := m.rows()[m.treeCursor]
				if r.account >= 0 && m.accounts[r.account].Config != nil {
					m.openAccountSetup(r.account)
				}
			}
		case "q":
			m.requestQuit()
		case "delete":
			m.requestDelete()
		case "\\", " ", "g", "d", "ctrl+w":
			m.prefix = key
		case "?":
			m.help = true
		case "j", "down":
			m.move(1)
		case "k", "up":
			m.move(-1)
		case "ctrl+d", "pgdown":
			m.move(max(1, (m.bodyHeight()-5)/2))
		case "ctrl+u", "pgup":
			m.move(-max(1, (m.bodyHeight()-5)/2))
		case "G":
			if m.focus == threadPane {
				m.scroll = 100000
			} else if m.focus == treePane {
				m.treeCursor = len(m.rows()) - 1
			} else {
				m.selected = len(m.folderThreads()) - 1
			}
		case "h", "left":
			m.focusLeft()
		case "l", "right":
			m.focusRight()
		case "enter":
			if m.focus == treePane {
				m.chooseFolder()
			} else {
				m.focusRight()
			}
		case "tab":
			if m.focus == treePane {
				m.focus = listPane
			} else if m.focus == listPane && m.reading {
				m.focus = threadPane
			} else if m.treeOpen {
				m.focus = treePane
			} else {
				m.focus = listPane
			}
		case "esc":
			m.query = ""
			m.reading = false
			m.focus = listPane
		case "/":
			m.prompt = "/"
			m.input = m.query
		case ":":
			m.prompt = ":"
			m.input = ""
		case "r":
			m.startReply()
		case "s":
			if t := m.current(); t != nil && t.QueueID == "" {
				if t.Remote {
					m.markLiveThread(t, "\\Flagged", !t.Starred)
				} else {
					t.Starred = !t.Starred
				}
			}
		case "u":
			if t := m.current(); t != nil && t.QueueID == "" {
				if t.Remote {
					m.live.readEpoch++
					m.markLiveThread(t, "\\Seen", t.Unread)
				} else {
					t.Unread = !t.Unread
				}
			}
		}
		m.normalizeSelection()
	}
	return m, nil
}
func (m *model) focusLeft() {
	if m.focus == threadPane {
		m.focus = listPane
		m.reading = false
		return
	}
	if m.focus == listPane {
		m.revealTree()
		return
	}
	r := m.rows()[m.treeCursor]
	if r.account < 0 {
		return
	}
	if r.folder == "" {
		m.expanded[r.account] = false
	} else {
		for i, row := range m.rows() {
			if row.account == r.account && row.folder == "" {
				m.treeCursor = i
				break
			}
		}
	}
}
func (m *model) focusRight() {
	if m.focus == treePane {
		r := m.rows()[m.treeCursor]
		if r.account < 0 {
			m.openAccountSetup(-1)
			return
		}
		if r.folder == "" {
			m.expanded[r.account] = true
		} else {
			m.chooseFolder()
		}
	} else if m.focus == listPane {
		m.openThread()
	}
}
func (m *model) click(x, y int) {
	if y < 4 || y >= m.height-2 {
		return
	}
	tw, lw, rw := m.layout()
	if tw > 0 && x < tw {
		m.focus = treePane
		i := y - 7 + m.treeOffset
		if i >= 0 && i < len(m.rows()) {
			m.treeCursor = i
			m.chooseFolder()
		}
	} else if lw > 0 && x < tw+lw {
		m.focus = listPane
		i := (y-7)/6 + m.listOffset
		if y >= 7 && i < len(m.folderThreads()) {
			m.selected = i
			m.openThread()
		}
	} else if rw > 0 {
		m.focus = threadPane
	}
	m.normalizeSelection()
}
func (m model) handlePrompt(key tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch key.String() {
	case "esc":
		m.prompt = ""
		m.input = ""
	case "backspace":
		r := []rune(m.input)
		if len(r) > 0 {
			m.input = string(r[:len(r)-1])
		}
	case "enter":
		if m.compose != nil {
			command := strings.TrimSpace(m.input)
			m.prompt = ""
			m.input = ""
			m.editorCommand(command)
			return m, nil
		}
		if m.prompt == "/" {
			m.query = m.input
			m.selected = 0
			m.listOffset = 0
			m.reading = false
			m.focus = listPane
		} else {
			switch strings.TrimSpace(m.input) {
			case "queue":
				m.openSyncQueue()
			case "delete", "trash":
				m.requestDelete()
			case "canceldelete":
				m.cancelUncertainDelete()
			case "q", "quit":
				m.requestQuit()
			case "q!":
				if m.compose != nil {
					m.compose = nil
					m.notice = "Reply discarded."
				} else {
					m.requestQuit()
				}
			case "help":
				m.help = true
			case "addaccount":
				m.openAccountSetup(-1)
			case "sync":
				m.syncCurrent(false)
			case "older":
				m.syncCurrent(true)
			case "new":
				m.startLiveCompose()
			case "passwords":
				m.prompt, m.input = "", ""
				return m.launchPasswordSetup()
			case "nt":
				m.toggleTree()
			case "nf":
				m.revealTree()
			case "w":
				m.saveDraft()
			case "send":
				m.sendDemo()
			case "noh":
				m.query = ""
			default:
				m.notice = "Unknown command. Try :help, :nt, :w, :send, or :q."
			}
		}
		m.prompt = ""
		m.input = ""
		m.normalizeSelection()
	default:
		if key.Text != "" {
			m.input += key.Text
		}
	}
	return m, nil
}
func (m *model) startReply() {
	t := m.current()
	if t == nil || t.QueueID != "" {
		return
	}
	returnID := t.ID
	if !isDraftThread(t) {
		if saved := m.savedReply(t.Account, t.ID); saved != nil {
			t = saved
		}
	}
	to := t.newest().Address
	for _, msg := range t.Messages {
		if msg.Address != m.accounts[t.Account].Address {
			to = msg.Address
			break
		}
	}
	subject := t.Subject
	if !isDraftThread(t) && !strings.HasPrefix(subject, "Re: ") {
		subject = "Re: " + subject
	}
	m.compose = &draft{account: t.Account, threadID: t.ID, to: to, subject: subject}
	if isDraftThread(t) {
		m.compose.body = t.newest().Body
		m.compose.threadID = t.ReplyTo
		m.compose.savedID = t.ID
		m.compose.to = t.Recipient
	}
	m.insert = false
	m.replyThreadFocus = false
	m.replyWindowPending = false
	m.replyThreadScroll = 0
	m.compose.savedBody = m.compose.body
	m.editorReturnID = returnID
	m.editorPending = ""
	m.prefix = ""
	m.editorHelp = false
	m.editorClose = false
	m.undo = nil
	m.redo = nil
	m.goalColumn = -1
	m.notice = "Ctrl+Y send demo · Ctrl+S save draft"
	if m.compose.savedID != 0 {
		m.notice = "Draft resumed · Ctrl+Y send demo · Ctrl+S save"
	}
	m.bodyCursor = len([]rune(m.compose.body))
	m.composeScroll = 0
	m.prepareLiveReply(t)
	if t.Remote && !t.LocalDraft {
		m.expandThread(t)
	}
}
func (m *model) saveDraft() bool {
	if m.compose != nil && m.realAccount(m.compose.account) {
		return m.saveLiveDraft()
	}
	if m.compose == nil {
		m.notice = "No reply to save."
		return false
	}
	d := m.compose
	id := d.savedID
	if id == 0 {
		id = m.nextID()
	}
	m.removeSavedDraft(d.savedID)
	m.threads = append(m.threads, Thread{ID: id, Account: d.account, Folder: "Drafts", Subject: d.subject, ReplyTo: d.threadID, Recipient: d.to, Messages: []Message{{"You", m.accounts[d.account].Address, d.body, time.Now()}}})
	normalize(m.threads)
	d.savedID = id
	d.savedBody = d.body
	m.notice = "Draft saved for this session. Ctrl+Q closes the editor."
	m.normalizeSelection()
	return true
}
func (m *model) removeSavedDraft(id int) {
	if id == 0 {
		return
	}
	for i, t := range m.threads {
		if t.ID == id && isDraftThread(&t) {
			m.threads = append(m.threads[:i], m.threads[i+1:]...)
			return
		}
	}
}
func (m model) nextID() int {
	n := 0
	for _, t := range m.threads {
		n = max(n, t.ID)
	}
	return n + 1
}
func (m *model) sendDemo() {
	if m.compose != nil && m.realAccount(m.compose.account) {
		m.sendLive(false)
		return
	}
	if m.compose == nil {
		m.notice = "Open a reply with r first."
		return
	}
	d := m.compose
	if strings.TrimSpace(d.body) == "" {
		m.notice = "Add a message before sending."
		return
	}
	msg := Message{"You", m.accounts[d.account].Address, d.body, time.Now()}
	m.removeSavedDraft(d.savedID)
	for i := range m.threads {
		if m.threads[i].ID == d.threadID {
			m.threads[i].Messages = append(m.threads[i].Messages, msg)
		}
	}
	m.threads = append(m.threads, Thread{ID: m.nextID(), Account: d.account, Folder: "Sent", Subject: d.subject, Messages: []Message{msg}})
	normalize(m.threads)
	m.closeCompose()
	m.scroll = 0
	m.notice = fmt.Sprintf("Demo reply added from %s. Nothing was transmitted.", m.accounts[d.account].Address)
}
