package main

import (
	"context"
	"strconv"
	"strings"
	"unicode"

	tea "charm.land/bubbletea/v2"
	lip "charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

const setupFields = 11

var setupLabels = []string{"Provider", "Name", "Email", "App password", "IMAP host", "IMAP port", "IMAP login", "SMTP host", "SMTP port", "SMTP login", "SMTP security"}

type accountSetup struct {
	insert                bool
	pending               string
	issue                 bool
	values                [setupFields]string
	cursor, focus, offset int
	id                    uint64
	edit                  int
	busy, success         bool
	message               string
	cancel                context.CancelFunc
}
type accountCheckMsg struct {
	id      uint64
	folders []string
	err     error
}

func (m *model) openAccountSetup(edit int) {
	m.setupID++
	f := &accountSetup{id: m.setupID, edit: edit, focus: 2}
	f.values[0] = "iCloud"
	f.values[1] = "iCloud"
	f.preset()
	if edit >= 0 && edit < len(m.accounts) && m.accounts[edit].Config != nil {
		c := m.accounts[edit].Config
		f.values = [setupFields]string{c.Provider, c.Name, c.Email, "", c.IMAPHost, strconv.Itoa(c.IMAPPort), c.IMAPUser, c.SMTPHost, strconv.Itoa(c.SMTPPort), c.SMTPUser, c.SMTPSecurity}
		f.focus = 3
	}
	m.setup = f
	m.prefix = ""
	m.prompt = ""
	m.input = ""
	if m.configError != "" {
		f.message = m.configError
		f.issue = true
	}
}
func (f *accountSetup) preset() {
	switch f.values[0] {
	case "iCloud":
		f.values[4] = "imap.mail.me.com"
		f.values[7] = "smtp.mail.me.com"
	case "Gmail":
		f.values[4] = "imap.gmail.com"
		f.values[7] = "smtp.gmail.com"
	case "Custom":
		f.values[4] = ""
		f.values[7] = ""
	}
	f.values[5] = "993"
	f.values[8] = "587"
	f.values[10] = "STARTTLS"
	f.values[6] = ""
	f.values[9] = ""
}
func (f *accountSetup) config() AccountConfig {
	port, _ := strconv.Atoi(strings.TrimSpace(f.values[5]))
	smtpPort, _ := strconv.Atoi(strings.TrimSpace(f.values[8]))
	c := AccountConfig{Name: strings.TrimSpace(f.values[1]), Email: strings.TrimSpace(f.values[2]), Provider: f.values[0], IMAPHost: strings.TrimSpace(f.values[4]), IMAPPort: port, IMAPUser: strings.TrimSpace(f.values[6]), SMTPHost: strings.TrimSpace(f.values[7]), SMTPPort: smtpPort, SMTPUser: strings.TrimSpace(f.values[9]), SMTPSecurity: f.values[10]}
	if c.IMAPUser == "" {
		c.IMAPUser = c.Email
		if c.Provider == "iCloud" {
			c.IMAPUser = strings.Split(c.Email, "@")[0]
		}
	}
	if c.SMTPUser == "" {
		c.SMTPUser = c.Email
	}
	return c
}
func (m *model) closeAccountSetup() {
	if m.setup != nil {
		if m.setup.cancel != nil {
			m.setup.cancel()
		}
		m.setup.values[3] = ""
	}
	m.setup = nil
}
func (m *model) connectAccount() tea.Cmd {
	f := m.setup
	f.insert = false
	f.pending = ""
	if f.busy {
		return nil
	}
	if f.success {
		m.closeAccountSetup()
		return nil
	}
	if m.configError != "" {
		f.message = m.configError
		f.issue = true
		return nil
	}
	c := f.config()
	if at, msg := c.validate(); msg != "" {
		f.focus = at
		f.cursor = len([]rune(f.values[at]))
		f.message = msg
		f.issue = true
		return nil
	}
	if strings.TrimSpace(f.values[3]) == "" {
		if f.edit >= 0 && m.accounts[f.edit].Config != nil {
			saved := m.accounts[f.edit].Config
			if saved.RememberPassword && credentialKey(*saved) == credentialKey(c) {
				c.RememberPassword = true
				c.BitwardenItem = saved.BitwardenItem
			}
		}
	}
	if strings.TrimSpace(f.values[3]) == "" && !c.RememberPassword {
		f.focus = 3
		f.cursor = 0
		f.message = "Enter an app password from your email provider."
		f.issue = true
		return nil
	}
	for i, a := range m.accounts {
		if i != f.edit && a.Config != nil && strings.EqualFold(a.Address, c.Email) {
			f.focus = 2
			f.message = "This email is already configured. Select its account and press e to edit."
			f.issue = true
			return nil
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	f.cancel = cancel
	f.busy = true
	f.issue = false
	f.message = "Checking IMAP sign-in, folders, and SMTP sign-in…"
	id, password, store := f.id, f.values[3], m.credentials
	// Providers display app passwords in groups; spaces are not part of them.
	if c.Provider == "Gmail" {
		password = strings.ReplaceAll(password, " ", "")
	}
	return func() tea.Msg {
		if c.RememberPassword {
			if store == nil {
				return accountCheckMsg{id: id, err: &connectionProblem{"Local password", "Run hane --password-setup first."}}
			}
			var err error
			password, err = store.Get(ctx, c)
			if err != nil {
				return accountCheckMsg{id: id, err: err}
			}
		}
		folders, err := checkAccount(ctx, c, password)
		return accountCheckMsg{id, folders, err}
	}
}
func (m model) finishAccountCheck(msg accountCheckMsg) (tea.Model, tea.Cmd) {
	f := m.setup
	if f == nil || f.id != msg.id || !f.busy {
		return m, nil
	}
	f.busy = false
	if f.cancel != nil {
		f.cancel()
		f.cancel = nil
	}
	if msg.err != nil {
		f.message = msg.err.Error()
		f.issue = true
		return m, nil
	}
	c := f.config()
	if f.edit >= 0 && m.accounts[f.edit].Config != nil {
		saved := m.accounts[f.edit].Config
		if credentialKey(*saved) == credentialKey(c) {
			c.BitwardenItem = saved.BitwardenItem
			c.RememberPassword = saved.RememberPassword && f.values[3] == ""
		}
	}
	c.Folders = msg.folders
	var configs []AccountConfig
	for i, a := range m.accounts {
		if a.Config != nil {
			if i == f.edit {
				configs = append(configs, c)
			} else {
				configs = append(configs, *a.Config)
			}
		}
	}
	if f.edit < 0 {
		configs = append(configs, c)
	}
	if err := saveAccountConfigs(m.configPath, configs); err != nil {
		f.message = "Sign-ins passed, but settings could not be saved. Check config directory permissions and retry."
		f.issue = true
		return m, nil
	}
	if f.edit >= 0 {
		a := &m.accounts[f.edit]
		a.Name = c.Name
		a.Address = c.Email
		a.Config = &c
		a.Folders = append([]string(nil), c.Folders...)
		a.Verified = true
		if len(a.Folders) == 0 {
			a.Folders = []string{"INBOX"}
		}
		m.account = f.edit
	} else {
		m.appendConfiguredAccount(c, true)
		m.account = len(m.accounts) - 1
	}
	m.folder = m.accounts[m.account].Folders[0]
	m.query = ""
	m.selected = 0
	m.listOffset = 0
	m.scroll = 0
	m.reading = false
	m.revealTree()
	m.normalizeSelection()
	if m.live != nil {
		if f.values[3] != "" {
			m.live.passwords[credentialKey(c)] = normalizePassword(c, f.values[3])
		}
		m.queueSync(m.account, syncRequest{Folder: inboxName(m.accounts[m.account])})
	}
	f.values[3] = ""
	f.success = true
	f.focus = setupFields
	f.message = "IMAP and SMTP sign-ins passed. Settings saved; your mail will sync automatically."
	if !c.RememberPassword {
		f.message += " Run hane --password-setup to remember this password through Bitwarden."
	}
	m.notice = "Account verified · settings saved"
	return m, nil
}
func (m model) handleAccountSetup(msg tea.Msg) (tea.Model, tea.Cmd) {
	f := m.setup
	previousFocus := f.focus
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		m.normalizeSelection()
	case tea.KeyPressMsg:
		k := msg.String()
		if k == "ctrl+c" || k == "ctrl+q" {
			m.closeAccountSetup()
			return m, nil
		}
		if k == "esc" {
			if f.insert {
				f.insert = false
			} else if f.pending != "" {
				f.pending = ""
			} else {
				m.closeAccountSetup()
			}
			return m, nil
		}
		if f.busy {
			return m, nil
		}
		if f.success {
			if k == "enter" || k == "q" {
				m.closeAccountSetup()
			}
			return m, nil
		}
		if !f.insert && f.pending != "" {
			if f.pending+k == "gg" {
				f.focus = 0
			}
			f.pending = ""
		} else {
			switch k {
			case "tab", "down", "ctrl+j":
				f.focus = (f.focus + 1) % (setupFields + 2)
				f.insert = false
			case "shift+tab", "up", "ctrl+k":
				f.focus = (f.focus + setupFields + 1) % (setupFields + 2)
				f.insert = false
			case "ctrl+enter":
				return m.startAccountCheck()
			case "enter":
				if f.focus == setupFields {
					return m.startAccountCheck()
				}
				if f.focus == setupFields+1 {
					m.closeAccountSetup()
					return m, nil
				}
				if f.focus == 0 || f.focus == 10 {
					f.cycle(1)
				} else if f.insert {
					f.focus++
					f.insert = false
				} else {
					f.insert = true
				}
			default:
				if !f.insert {
					switch k {
					case "q":
						m.closeAccountSetup()
						return m, nil
					case "j":
						f.focus = min(setupFields+1, f.focus+1)
					case "k":
						f.focus = max(0, f.focus-1)
					case "g":
						f.pending = "g"
					case "G":
						f.focus = setupFields + 1
					case "i", "a", "I", "A":
						if f.textField() {
							f.insert = true
							if k == "I" {
								f.cursor = 0
							}
							if k == "A" {
								f.cursor = len([]rune(f.values[f.focus]))
							}
							if k == "a" {
								f.cursor = min(f.cursor+1, len([]rune(f.values[f.focus])))
							}
						}
					case "h", "left":
						f.horizontal(-1)
					case "l", "right":
						f.horizontal(1)
					case "0", "home":
						if f.textField() {
							f.cursor = 0
						}
					case "$", "end":
						if f.textField() {
							f.cursor = len([]rune(f.values[f.focus]))
						}
					case " ":
						if f.focus == 0 || f.focus == 10 {
							f.cycle(1)
						}
					}
				} else if f.textField() {
					r := []rune(f.values[f.focus])
					f.cursor = max(0, min(f.cursor, len(r)))
					switch k {
					case "left":
						f.horizontal(-1)
					case "right":
						f.horizontal(1)
					case "home", "ctrl+a":
						f.cursor = 0
					case "end", "ctrl+e":
						f.cursor = len(r)
					case "ctrl+u":
						f.values[f.focus] = ""
						f.cursor = 0
					case "backspace":
						if f.cursor > 0 {
							f.values[f.focus] = string(r[:f.cursor-1]) + string(r[f.cursor:])
							f.cursor--
						}
					case "delete":
						if f.cursor < len(r) {
							f.values[f.focus] = string(r[:f.cursor]) + string(r[f.cursor+1:])
						}
					default:
						if msg.Text != "" {
							f.insertText(msg.Text)
						}
					}
				}
			}
		}
	case tea.PasteMsg:
		if !f.busy && !f.success && f.insert && f.textField() {
			f.insertText(msg.Content)
		}
	case tea.MouseClickMsg:
		if msg.Button != tea.MouseLeft {
			return m, nil
		}
		x, y, w, h := m.setupGeometry()
		if msg.X < x+1 || msg.X >= x+w-1 {
			return m, nil
		}
		if msg.Y == y+h-3 {
			if msg.X >= x+w/2 {
				m.closeAccountSetup()
				return m, nil
			} else if !f.busy {
				return m.startAccountCheck()
			}
		} else if !f.busy && !f.success && msg.Y >= y+4 && msg.Y < y+4+m.setupVisible() {
			at := m.setupOffset() + msg.Y - y - 4
			if at < setupFields {
				f.insert = false
				f.pending = ""
				f.focus = at
				f.cursor = len([]rune(f.values[at]))
			}
		}
	}
	if f.focus != previousFocus && f.focus < setupFields {
		f.cursor = len([]rune(f.values[f.focus]))
	}
	f.offset = m.setupOffset()
	return m, nil
}
func (f *accountSetup) cycle(delta int) {
	if f.focus == 10 {
		if f.values[10] == "TLS" {
			f.values[10] = "STARTTLS"
			f.values[8] = "587"
		} else {
			f.values[10] = "TLS"
			f.values[8] = "465"
		}
		return
	}
	previous := f.values[0]
	providers := []string{"iCloud", "Gmail", "Custom"}
	for i, p := range providers {
		if p == f.values[0] {
			f.values[0] = providers[(i+delta+3)%3]
			break
		}
	}
	if f.values[1] == previous {
		f.values[1] = f.values[0]
	}
	f.preset()
}
func (f *accountSetup) insertText(s string) {
	s = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return -1
		}
		return r
	}, s)
	r := []rune(f.values[f.focus])
	f.cursor = max(0, min(f.cursor, len(r)))
	if len(r)+len([]rune(s)) > 512 {
		f.message = "This field is limited to 512 characters."
		f.issue = true
		return
	}
	f.values[f.focus] = string(r[:f.cursor]) + s + string(r[f.cursor:])
	f.cursor += len([]rune(s))
}
func (m model) setupGeometry() (x, y, w, h int) {
	w = max(1, min(84, m.width-2))
	h = max(1, min(25, m.height-2))
	return (m.width - w) / 2, (m.height - h) / 2, w, h
}
func (m model) setupVisible() int {
	_, _, _, h := m.setupGeometry()
	return max(1, h-8-len(m.setupMessageLines()))
}
func (m model) setupOffset() int {
	f := m.setup
	at := min(f.focus, setupFields-1)
	return max(0, min(max(f.offset, at-m.setupVisible()+1), at))
}
func (m model) overlayAccountSetup(lines []string) []string {
	f := m.setup
	x, y, w, h := m.setupGeometry()
	inner := max(0, w-2)
	rows := make([]string, h)
	for i := range rows {
		rows[i] = styled("│", violet) + strings.Repeat(" ", inner) + styled("│", violet)
	}
	put := func(at int, s string) {
		if at > 0 && at < h-1 {
			rows[at] = styled("│", violet) + fit(s, inner) + styled("│", violet)
		}
	}
	title := "Add Account"
	if f.edit >= 0 {
		title = "Account settings"
	}
	if f.success {
		title = "Account verified"
	}
	if !f.success {
		mode := "Normal"
		if f.insert {
			mode = "Insert"
		}
		if f.busy {
			mode = "Connecting"
		}
		title += " · " + mode
	}
	put(1, center(bold(title, cyan), inner))
	subtitle := "Password setup: hane --password-setup"
	if f.edit >= 0 && m.accounts[f.edit].Config.RememberPassword {
		subtitle = "Leave App password blank to use your local keyring"
	}
	if f.values[0] == "Gmail" {
		subtitle = "Gmail app password requires 2-Step Verification"
	}
	put(2, styled(subtitle, muted))
	put(3, gradient(strings.Repeat("─", inner), 0))
	offset := m.setupOffset()
	for at := offset; at < min(setupFields, offset+m.setupVisible()); at++ {
		label := fit(setupLabels[at], 14)
		value := f.values[at]
		if at == 3 {
			value = strings.Repeat("•", len([]rune(value)))
			if value == "" && f.edit >= 0 && m.accounts[f.edit].Config.RememberPassword && !activePasswordField(f) {
				value = "(leave blank to use keyring)"
			}
		}
		if value == "" && (at == 6 || at == 9) {
			value = "(automatic from email)"
		}
		active := f.focus == at && !f.busy && !f.success
		width := max(1, inner-16)
		if active && at != 0 && at != 10 {
			value, _ = f.fieldViewport(width)
		} else if at == 0 || at == 10 {
			value = "‹ " + value + " ›"
		}
		row := styled(label, muted) + styled(fit(value, width), text)
		if active {
			row = lip.NewStyle().Background(selection).Render(row)
		}
		put(4+at-offset, " "+row)
	}
	wrapped := m.setupMessageLines()
	statusColor := oceanText
	if f.issue {
		statusColor = lip.Color("#f0a6c3")
	}
	for i, line := range wrapped {
		put(h-4-len(wrapped)+i, " "+styled(line, statusColor))
	}
	connect := "Connect & save"
	cancel := "Cancel"
	if f.busy {
		connect = "Connecting…"
	}
	if f.success {
		connect = "Done"
		cancel = "Close"
	}
	button := func(label string, active bool) string {
		st := lip.NewStyle().Foreground(cyan).Background(bluePane)
		if active {
			st = st.Foreground(ink).Background(cyan).Bold(true)
		}
		return st.Render(" " + label + " ")
	}
	put(h-3, fit(button(connect, f.focus == setupFields), inner/2)+button(cancel, f.focus == setupFields+1))
	put(h-2, styled(f.hint(), muted))
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

// Configured accounts currently expose discovered folders, with no message sync.
func (m model) configuredAccount() bool { return m.accounts[m.account].Config != nil }
func (m model) configuredStatus() string {
	if m.realAccount(m.account) {
		s := m.accounts[m.account].ConnectionStatus
		if s != "" {
			return s
		}
		return "Cached mail · R to sync"
	}
	if m.accounts[m.account].Verified {
		return "Sign-in verified · mail sync coming next"
	}
	if s := m.accounts[m.account].ConnectionStatus; s != "" {
		return s
	}
	return "Saved account · select account + e to reconnect"
}

func activePasswordField(f *accountSetup) bool          { return f.focus == 3 && !f.busy && !f.success }
func (m model) startAccountCheck() (tea.Model, tea.Cmd) { cmd := m.connectAccount(); return m, cmd }

func (m model) setupMessageLines() []string {
	_, _, w, h := m.setupGeometry()
	message := m.setup.message
	if message == "" {
		message = "IMAP uses TLS. Connect checks sign-in only; no mail is sent."
	}
	lines := strings.Split(ansi.Wrap(message, max(1, w-4), ""), "\n")
	for len(lines) < 2 {
		lines = append(lines, "")
	}
	return lines[:min(len(lines), max(1, min(4, h-9)))]
}

func (f *accountSetup) textField() bool { return f.focus > 0 && f.focus < 10 }
func (f *accountSetup) horizontal(delta int) {
	if f.focus == 0 || f.focus == 10 {
		f.cycle(delta)
	} else if f.focus >= setupFields {
		f.focus = setupFields + (f.focus-setupFields+1)%2
	} else {
		f.cursor = max(0, min(f.cursor+delta, len([]rune(f.values[f.focus]))))
	}
}
func (f *accountSetup) fieldViewport(width int) (string, int) {
	value := f.values[f.focus]
	if f.focus == 3 {
		value = strings.Repeat("•", len([]rune(value)))
	}
	r := []rune(value)
	cursor := max(0, min(f.cursor, len(r)))
	col := ansi.StringWidth(string(r[:cursor]))
	offset := max(0, col-width+1)
	return ansi.Cut(value, offset, offset+width), col - offset
}
func (m model) setupCursor() *tea.Cursor {
	f := m.setup
	if f == nil || f.busy || f.success || !f.textField() || m.width < 48 || m.height < 16 {
		return nil
	}
	x, y, w, _ := m.setupGeometry()
	_, col := f.fieldViewport(max(1, w-18))
	cursor := tea.NewCursor(x+16+col, y+4+f.focus-m.setupOffset())
	cursor.Color = cyan
	cursor.Shape = tea.CursorBlock
	cursor.Blink = false
	if f.insert {
		cursor.Shape = tea.CursorBar
		cursor.Blink = true
	}
	return cursor
}

func (f *accountSetup) hint() string {
	hint := "j/k fields · i edit · Enter select · q close"
	if f.focus == 0 || f.focus == 10 {
		hint = "h/l choose · j/k fields · Esc close"
	}
	if f.insert {
		hint = "Esc normal · Tab next · Ctrl+u clear"
	}
	if f.pending != "" {
		hint = "g … g first field · Esc cancel key sequence"
	}
	if f.busy {
		hint = "Testing IMAP + SMTP · Esc cancels immediately"
	}
	if f.success {
		hint = "Enter / Esc returns to your accounts"
	}
	return hint
}
