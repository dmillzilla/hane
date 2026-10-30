package app

import (
	"fmt"
	"image/color"
	"math"
	"strings"

	tea "charm.land/bubbletea/v2"
	lip "charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

var (
	ink        = lip.Color("#0b162b")
	oceanPane  = lip.Color("#0b252e")
	bluePane   = lip.Color("#10243e")
	violetPane = lip.Color("#201e3e")
	statusPane = lip.Color("#17364a")
	text       = lip.Color("#b5d5ff")
	muted      = lip.Color("#89b9e3")
	dim        = lip.Color("#789dcc")
	oceanText  = lip.Color("#8be2d3")
	readerText = lip.Color("#d0c7ff")
	teal       = lip.Color("#2dd4bf")
	cyan       = lip.Color("#67dafa")
	violet     = lip.Color("#b19aff")
	selection  = lip.Color("#254665")
)

func styled(s string, c color.Color) string {
	return lip.NewStyle().Foreground(c).Render(s)
}
func bold(s string, c color.Color) string {
	return lip.NewStyle().Bold(true).Foreground(c).Render(s)
}

// Nested text styles reset their background. Restore the containing surface
// after those resets so colors fill the pane, including its empty space.
func onSurface(s string, bg color.Color) string {
	r, g, b, _ := bg.RGBA()
	base := fmt.Sprintf("\x1b[48;2;%d;%d;%dm", r>>8, g>>8, b>>8)
	restore := strings.NewReplacer("\x1b[0m", "\x1b[0m"+base, "\x1b[m", "\x1b[m"+base, "\x1b[49m", base)
	return base + restore.Replace(s) + "\x1b[0m"
}
func fit(s string, w int) string {
	if w <= 0 {
		return ""
	}
	s = ansi.Truncate(s, w, "…")
	return s + strings.Repeat(" ", max(0, w-ansi.StringWidth(s)))
}
func joinSides(left, right string, w int) string {
	right = ansi.Truncate(right, max(0, w/2), "…")
	return fit(left, max(0, w-ansi.StringWidth(right))) + right
}
func gradient(s string, phase float64) string {
	stops := []color.RGBA{{20, 184, 166, 255}, {64, 215, 237, 255}, {82, 149, 245, 255}, {167, 139, 250, 255}}
	r := []rune(s)
	var out strings.Builder
	for i, ch := range r {
		x := float64(i) / float64(max(1, len(r)-1)) * 3
		if phase != 0 {
			x = max(0, min(3, x+math.Sin(phase+float64(i)*0.07)*0.35))
		}
		a := min(2, int(x))
		t := x - float64(a)
		lo, hi := stops[a], stops[a+1]
		c := color.RGBA{uint8(float64(lo.R)*(1-t) + float64(hi.R)*t), uint8(float64(lo.G)*(1-t) + float64(hi.G)*t), uint8(float64(lo.B)*(1-t) + float64(hi.B)*t), 255}
		out.WriteString(styled(string(ch), c))
	}
	return out.String()
}
func (m model) bodyHeight() int { return max(1, m.height-6) }
func (m model) layout() (tree, list, reader int) {
	available := m.width
	if m.treeOpen {
		if m.width < 90 && m.focus == treePane {
			return m.width, 0, 0
		}
		if m.width >= 90 {
			tree = 29
			available -= tree
		}
	}
	if m.reading {
		if available >= 96 {
			list = 43
			reader = available - list
		} else {
			reader = available
		}
	} else if available >= 112 {
		list = 54
		reader = available - list
	} else {
		list = available
	}
	return
}
func (m model) readerWidth() int { _, _, w := m.layout(); return max(20, w-5) }
func (m model) readerLines() []string {
	if t := m.current(); t != nil && t.QueueID != "" {
		return m.queueReaderLines(t, m.readerWidth())
	}
	return threadLines(m.current(), m.readerWidth())
}
func threadLines(t *Thread, w int) []string {
	if t == nil {
		return nil
	}
	var out []string
	for i, msg := range t.Messages {
		label := "Earlier"
		if i == 0 {
			label = "Latest"
		}
		out = append(out, joinSides(bold(msg.Sender, cyan), styled(msg.Received.Local().Format("Jan 02 · 15:04"), muted), w))
		out = append(out, styled(msg.Address, muted), styled(fmt.Sprintf("%s  /  %02d", label, i+1), violet), "")
		for _, line := range strings.Split(ansi.Wrap(msg.Body, w, ""), "\n") {
			out = append(out, styled(line, readerText))
		}
		out = append(out, "", gradient(strings.Repeat("─", w), 0), "")
	}
	return out
}
func (m *model) clampScroll() {
	m.scroll = max(0, min(m.scroll, max(0, len(m.readerLines())-(m.bodyHeight()-4))))
}

func paneLines(lines []string, w, h int, separator bool, surface ...color.Color) string {
	var bg color.Color = ink
	if len(surface) > 0 {
		bg = surface[0]
	}
	inner := max(1, w-2)
	if separator {
		inner--
	}
	var out []string
	for y := 0; y < h; y++ {
		line := ""
		if y < len(lines) {
			line = lines[y]
		}
		row := styled(" ", ink) + fit(line, inner) + styled(" ", ink)
		if separator {
			row += styled("│", dim)
		}
		out = append(out, onSurface(fit(row, w), bg))
	}
	return strings.Join(out, "\n")
}
func (m model) treeView(w int) string {
	c := oceanText
	if m.focus == treePane {
		c = teal
	}
	lines := []string{bold("Explorer", c), styled("Folders · unread counts", oceanText), styled(strings.Repeat("─", max(1, w-3)), teal)}
	rows := m.rows()
	for i := m.treeOffset; i < len(rows); i++ {
		r := rows[i]
		label := ""
		c := oceanText
		if r.account == 1 {
			c = violet
		}
		if r.account < 0 {
			label = "[ + Add Account ]"
			c = cyan
		} else if r.folder == "" {
			arrow := "▾"
			if !m.expanded[r.account] {
				arrow = "▸"
			}
			label = arrow + " " + m.accounts[r.account].Name
			if m.accounts[r.account].Config != nil {
				if m.live != nil {
					if m.live.busy[m.liveKey(r.account)] {
						label += " · syncing"
					} else {
						label += " · mail"
					}
				} else {
					label += " · setup"
				}
			} else {
				label += " · demo"
			}
			if r.account == 0 {
				c = teal
			} else {
				c = violet
			}
		} else {
			mark := "  "
			if r.account == m.account && r.folder == m.folder {
				mark = "› "
			}
			label = fmt.Sprintf("  %s%-10s %2d", mark, r.folder, m.unreadCount(r.account, r.folder))
			if m.accounts[r.account].Config != nil {
				label = fmt.Sprintf("  %s%-10s  —", mark, r.folder)
				if m.realAccount(r.account) {
					if n, ok := m.liveUnread(r.account, r.folder); ok {
						label = fmt.Sprintf("  %s%-10s %2d", mark, r.folder, n)
					}
				}
			}
			if r.folder == syncQueueFolder && m.realAccount(r.account) {
				count := len(m.live.disk.Accounts[m.liveKey(r.account)].Trash)
				label = fmt.Sprintf("  %sSync queue [%d]", mark, count)
				c = violet
			}
		}
		if i == m.treeCursor && m.focus == treePane {
			lines = append(lines, lip.NewStyle().Background(selection).Foreground(c).Bold(true).Render(fit(label, w-3)))
		} else {
			lines = append(lines, styled(label, c))
		}
	}
	return paneLines(lines, w, m.bodyHeight(), w < m.width, oceanPane)
}
func (m model) listView(w int) string {
	list := m.folderThreads()
	c := muted
	if m.focus == listPane {
		c = cyan
	}
	unread := countUnread(list)
	if m.realAccount(m.account) {
		if n, ok := m.liveUnread(m.account, m.folder); ok {
			unread = n
		}
	}
	addressLine := styled(m.accounts[m.account].Address, muted)
	if m.realAccount(m.account) {
		addressLine = joinSides(addressLine, styled(m.folderProgress(), muted), w-3)
	}
	lines := []string{joinSides(bold(m.folder, c), styled(fmt.Sprintf("%d unread ↓", unread), muted), w-3), addressLine, gradient(strings.Repeat("─", max(1, w-3)), 0)}
	if m.folder == syncQueueFolder && m.realAccount(m.account) {
		count := len(m.live.disk.Accounts[m.liveKey(m.account)].Trash)
		lines[0] = joinSides(bold("Sync queue", c), styled(fmt.Sprintf("%d pending", count), violet), w-3)
	}
	if len(list) == 0 {
		lines = append(lines, "", bold("A little breathing room.", cyan), styled("No messages in this view.", muted))
		if m.folder == syncQueueFolder && m.realAccount(m.account) {
			lines = append(lines[:3], "", bold("Queue clear", cyan), styled("No Trash moves are waiting to sync.", muted))
		} else if m.realAccount(m.account) {
			if len(m.live.disk.Accounts[m.liveKey(m.account)].Trash) > 0 {
				lines = append(lines, styled("Q shows pending Trash moves.", violet))
			}
		} else if m.configuredAccount() {
			lines = []string{bold(m.folder, cyan), styled(m.accounts[m.account].Address, muted), gradient(strings.Repeat("─", max(1, w-3)), 0), "", bold("Account configured", cyan)}
			for _, line := range strings.Split(ansi.Wrap(m.configuredStatus(), max(1, w-3), ""), "\n") {
				lines = append(lines, styled(line, muted))
			}
			lines = append(lines, styled("R sync · L older · c new message · B passwords", cyan))
			if m.query != "" {
				lines = append(lines, styled("No cached matches · Esc clears search", cyan))
			}
		} else if m.query != "" {
			lines = append(lines, styled("Esc clears your search.", muted))
		}
	}
	for i := m.listOffset; i < min(len(list), m.listOffset+max(1, (m.bodyHeight()-3)/6)); i++ {
		t := list[i]
		msg := t.newest()
		iw := max(8, w-5)
		marker := " "
		if t.Unread {
			marker = "●"
		}
		star := ""
		if t.Starred {
			star = " ★"
		}
		sender := msg.Sender + star
		pre := strings.Join(strings.Fields(msg.Body), " ")
		preview := strings.Split(ansi.Wrap(pre, iw, ""), "\n")
		for len(preview) < 2 {
			preview = append(preview, "")
		}
		subject, detail := t.Subject, m.conversationLabel(t)
		if isDraftThread(t) || m.savedReply(t.Account, t.ID) != nil {
			badge := lip.NewStyle().Foreground(ink).Background(violet).Bold(true).Render(" ✎ Draft ")
			subject = joinSides(subject, badge, iw)
			detail = "r resume draft · " + threadLabel(t)
			if isDraftThread(t) {
				detail = "Enter to resume · session only"
				if t.LocalDraft {
					detail = "Enter to resume · saved on this device"
				}
			}
		}
		if op, ok := m.queueOperation(t); ok {
			sender = m.trashStatus(t.Account, op)
			preview = []string{fmt.Sprintf("%d message(s) → %s", len(op.Refs), op.Destination), "Enter details · R retry"}
			if len(op.Refs) > 0 {
				preview[0] = op.Refs[0].Folder + " → " + op.Destination
			}
			detail = "Removed from mail views"
		}
		rows := []string{joinSides(sender, styled(stamp(msg.Received, m.now), violet), iw), subject, preview[0], preview[1], detail}
		selected := i == m.selected
		for j, row := range rows {
			prefix := "  "
			if j == 0 {
				prefix = marker + " "
			}
			if selected && j == 0 {
				prefix = "▎ "
			}
			col := muted
			if j == 0 {
				col = teal
			}
			if j == 1 {
				col = text
			}
			if j == 4 {
				col = violet
			}
			bg := bluePane
			if selected {
				bg = selection
				if j == 0 {
					col = cyan
				}
				if j == 4 {
					col = violet
				}
			}
			st := lip.NewStyle().Foreground(col).Background(bg)
			if j == 0 || j == 1 {
				st = st.Bold(true)
			}
			lines = append(lines, st.Render(fit(prefix+ansi.Truncate(row, iw, "…"), w-3)))
		}
		lines = append(lines, "")
	}
	_, _, rw := m.layout()
	return paneLines(lines, w, m.bodyHeight(), rw > 0, bluePane)
}
func (m model) readerView(w int) string {
	if m.configuredAccount() && !m.realAccount(m.account) {
		return paneLines([]string{bold("Account setup", cyan), styled(m.configuredStatus(), muted), "", styled("Your server folders are listed in Explorer.", text), styled("Message sync is not enabled yet.", muted), "", styled("B password setup · e reconnect in explorer", cyan)}, w, m.bodyHeight(), false, violetPane)
	}
	if !m.reading {
		lines := []string{styled("The reading room", muted), styled("A little more space for your mail.", dim), styled(strings.Repeat("─", max(1, w-2)), dim)}
		for len(lines) < max(5, m.bodyHeight()/3) {
			lines = append(lines, "")
		}
		for _, s := range []string{"      .________________.", "      |\\              /|", "      |  \\          /  |", "      |    \\______/    |", "      |________________|"} {
			lines = append(lines, gradient(s, 0))
		}
		lines = append(lines, "", bold("  Letters, at your own pace.", text), "", styled("  j / k     find your next conversation", muted), styled("  l / Enter open the entire thread", muted), styled("  \\nt      bring the explorer along", muted), "", styled("  Sample mail · Two accounts", dim))
		return paneLines(lines, w, m.bodyHeight(), false, violetPane)
	}
	t := m.current()
	if t == nil {
		return paneLines([]string{styled("No conversation selected", muted)}, w, m.bodyHeight(), false, violetPane)
	}
	c := muted
	if m.focus == threadPane {
		c = violet
	}
	lines := []string{bold(t.Subject, c), styled(m.conversationLabel(t)+" · newest reply first", muted), gradient(strings.Repeat("─", max(1, w-2)), 0)}
	if op, ok := m.queueOperation(t); ok {
		lines[1] = styled(m.trashStatus(t.Account, op), cyan)
	}
	content := m.readerLines()
	start := min(m.scroll, len(content))
	end := min(len(content), start+m.bodyHeight()-4)
	for _, line := range content[start:end] {
		lines = append(lines, " "+line)
	}
	for len(lines) < m.bodyHeight()-1 {
		lines = append(lines, "")
	}
	percent := 100
	if maxScroll := len(content) - (m.bodyHeight() - 4); maxScroll > 0 {
		percent = min(100, m.scroll*100/maxScroll)
	}
	lines = append(lines, styled(fmt.Sprintf(" r reply   h inbox                   %d%%", percent), dim))
	if t.QueueID != "" {
		lines[len(lines)-1] = styled(" R retry   h back to queue", dim)
	}
	return paneLines(lines, w, m.bodyHeight(), false, violetPane)
}

func (m model) View() tea.View {
	var content string
	if m.width < 48 || m.height < 16 {
		content = styled(appName+" · terminal too small\nResize to at least 48 × 16.\nq or Ctrl+C to quit", cyan)
	} else if m.loading {
		content = m.splash()
	} else {
		header := []string{gradient(strings.Repeat("━", m.width), 0), joinSides(" "+bold(appName, cyan)+styled("  /  a quiet place for mail", muted), styled("Demo  ·  v"+version+" ", violet), m.width), "", joinSides(styled(" "+m.accounts[m.account].Name+"  /  "+m.folder, muted), styled("newest first  ↓ ", dim), m.width)}
		if m.configuredAccount() {
			header[1] = joinSides(" "+bold(appName, cyan), styled("Mail · v"+version+" ", violet), m.width)
		}
		if m.compose != nil {
			header[3] = joinSides(styled(" Writing · "+m.accounts[m.account].Name, muted), styled(m.notice, oceanText), m.width)
		}
		body := ""
		if m.help {
			body = m.helpView()
		} else if m.compose != nil {
			body = m.composeView()
		} else {
			tw, lw, rw := m.layout()
			var panes []string
			if tw > 0 {
				panes = append(panes, m.treeView(tw))
			}
			if lw > 0 {
				panes = append(panes, m.listView(lw))
			}
			if rw > 0 {
				panes = append(panes, m.readerView(rw))
			}
			body = lip.JoinHorizontal(lip.Top, panes...)
		}
		mode := "Normal"
		if m.compose != nil && m.insert {
			mode = "Insert"
		}
		if m.prompt == "/" {
			mode = "Search"
		}
		if m.prompt == ":" {
			mode = "Command"
		}
		badge := lip.NewStyle().Foreground(ink).Background(teal).Bold(true).Render(" " + mode + " ")
		status := onSurface(joinSides(badge+styled("  "+m.notice, oceanText), styled(fmt.Sprintf(" %d/%d  ", min(m.selected+1, len(m.folderThreads())), len(m.folderThreads())), cyan), m.width), statusPane)
		hint := styled(" dd trash   r reply   / search   \\nt tree   h j k l navigate   ? help   :q quit", muted)
		if m.configuredAccount() || m.focus == treePane {
			hint = styled(" dd trash   Q queue   r reply   R sync   L older   c new   \\nt tree   ? help", muted)
		}
		if m.focus == treePane {
			hint = styled(" Enter open/toggle   h/l collapse/expand   j/k move   A add account   e settings", muted)
		}
		if m.compose != nil {
			hint = styled(" Esc normal   i insert   Ctrl+S save draft   :send simulate reply   :q! discard", muted)
		}
		if m.prefix != "" {
			hint = styled(" "+m.prefix+" …", cyan)
		}
		if m.query != "" && m.prompt == "" {
			hint = styled(" dd trash · /"+m.query+" · Esc clears search · ? help", cyan)
		}
		if m.folder == syncQueueFolder && m.compose == nil && m.focus != treePane {
			hint = styled(" j/k navigate   Enter details   R retry   h back   \\nt tree   ? help", muted)
			if op, ok := m.queueOperation(m.current()); ok && (op.State == "uncertain" || op.State == "copying" && !m.live.busy[m.liveKey(m.account)]) {
				hint = styled(" Enter details   :canceldelete show originals   h back   \\nt tree", cyan)
			}
		}
		if m.prompt != "" {
			hint = styled(" "+m.prompt+m.input+"▏", cyan)
		}
		if m.compose != nil {
			status, hint = m.editorFooter()
		}
		content = strings.Join(header, "\n") + "\n" + body + "\n" + fit(status, m.width) + "\n" + fit(hint, m.width)
	}
	// Fill the entire alternate screen, including sparse and narrow layouts.
	var lines []string
	for _, line := range strings.Split(content, "\n") {
		lines = append(lines, onSurface(fit(line, m.width), ink))
	}
	for len(lines) < m.height {
		lines = append(lines, lip.NewStyle().Background(ink).Render(strings.Repeat(" ", m.width)))
	}
	if len(lines) > m.height {
		lines = lines[:m.height]
	}
	if m.quitOpen {
		lines = m.overlayQuit(lines)
	}
	if m.deletion != nil {
		lines = m.overlayDelete(lines)
	}
	if m.editorClose {
		lines = m.overlayEditorClose(lines)
	}
	if m.setup != nil {
		lines = m.overlayAccountSetup(lines)
		if m.height >= 16 {
			mode := "Normal"
			if m.setup.insert {
				mode = "Insert"
			}
			if m.setup.busy {
				mode = "Connecting"
			}
			if m.setup.success {
				mode = "Verified"
			}
			badge := lip.NewStyle().Foreground(ink).Background(cyan).Bold(true).Render(" " + mode + " ")
			lines[m.height-2] = onSurface(fit(badge+styled(" Account setup", oceanText), m.width), statusPane)
			lines[m.height-1] = onSurface(fit(styled(" "+m.setup.hint(), muted), m.width), ink)
		}
	}
	v := tea.NewView(strings.Join(lines, "\n"))
	v.AltScreen = true
	v.MouseMode = tea.MouseModeCellMotion
	v.Cursor = m.replyCursor()
	if m.setup != nil {
		v.Cursor = m.setupCursor()
	}
	return v
}

func (m model) splash() string {
	art := logo()
	artWidth := 0
	for _, line := range art {
		artWidth = max(artWidth, ansi.StringWidth(line))
	}
	if m.width < artWidth+4 {
		art = []string{"H A N E"}
		artWidth = ansi.StringWidth(art[0])
	}
	var lines []string
	for len(lines) < max(0, (m.height-len(art)-7)/2) {
		lines = append(lines, "")
	}
	left := strings.Repeat(" ", max(0, (m.width-artWidth)/2))
	for _, line := range art {
		// A shared width keeps the artwork and gradient aligned across every row.
		lines = append(lines, left+gradient(fit(line, artWidth), float64(m.frame)/4))
	}
	lines = append(lines, "", center(styled("v"+version+"  /  your terminal courier", muted), m.width), "")
	barW := min(36, m.width-10)
	done := min(barW, m.frame*barW/20)
	bar := gradient(strings.Repeat("━", done), float64(m.frame)/4) + styled(strings.Repeat("─", barW-done), dim)
	lines = append(lines, center(bar, m.width), "", center(styled("Preparing your reading room", muted), m.width), center(styled("sample mail · press any key to enter", dim), m.width))
	return strings.Join(lines, "\n")
}
func center(s string, w int) string {
	return strings.Repeat(" ", max(0, (w-ansi.StringWidth(s))/2)) + s
}

func (m model) helpView() string {
	lines := []string{bold("The keymap", cyan), styled("A familiar rhythm. Press any key to return.", muted), gradient(strings.Repeat("─", m.width-4), 0), ""}
	for _, row := range [][2]string{
		{"\\nt / Space n t", "Toggle the account and folder explorer"},
		{"R / F5 / :sync", "Refresh mail, read status, and pending Sent copies"},
		{"L / :older", "Load 100 older messages from this folder"},
		{"c / :new", "Compose new mail; :to recipient and :subject subject"},
		{"B / :passwords", "Set up or refresh a Bitwarden-backed mail password"},
		{"\\nf", "Reveal the current folder in the explorer"},
		{"j / k", "Next / previous message, tree row, or thread line"},
		{"h / l", "Go back / open; collapse / expand accounts"},
		{"Enter / click", "Toggle account, open folder or conversation"},
		{"gg / G", "Jump to the top / bottom"},
		{"Ctrl+d / Ctrl+u", "Half-page down / up"},
		{"Tab / Ctrl+w h,l", "Move focus between panes"},
		{"/ / Esc", "Search folder contents / clear or go back"},
		{"s / u", "Toggle star / unread"},
		{"dd / Delete / :delete", "Move cached mail in this folder to Trash; delete local draft"},
		{"Q / :queue", "View pending Trash moves, failures, and recovery details"},
		{"r", "Reply or re-open a saved draft"},
		{"A / :addaccount", "Add account; also at the bottom of Explorer"},
		{"e on account", "Edit settings and check sign-in again"},
		{"Normal: :w", "Save draft; configured accounts also autosave"},
		{"Normal: :send", "Send mail (simulated only in demo accounts)"},
		{"Normal: :q!", "Discard the open reply"},
		{"q / :q / Ctrl+c", "Quit the prototype"},
	} {
		lines = append(lines, bold(fit(row[0], 24), teal)+styled(row[1], text))
	}
	note := "Demo mail is fictional. Changes reset when the app closes."
	if m.realAccount(m.account) {
		note = "Real mail · drafts saved locally · dd moves current-folder mail to Trash"
	}
	lines = append(lines, "", styled(note, muted))
	return paneLines(lines, m.width, m.bodyHeight(), false, bluePane)
}
