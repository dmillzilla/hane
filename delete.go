package main

import (
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"
	lip "charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/emersion/go-imap"
)

type deleteDialog struct {
	account, threadID                 int
	key, folder, subject, destination string
	refs                              []mailRef
	draft, selected                   bool
}

func refsOverlap(a, b []mailRef) bool {
	for _, x := range a {
		for _, y := range b {
			if x == y {
				return true
			}
		}
	}
	return false
}
func trashWorkPending(ops []trashChange) bool {
	for _, op := range ops {
		if op.State != "uncertain" {
			return true
		}
	}
	return false
}
func trashFolder(a accountMail) string {
	for _, f := range a.Folders {
		if f.Role == imap.TrashAttr {
			return f.Name
		}
	}
	return ""
}
func draftDeliveryPending(a accountMail, id int) bool {
	for _, o := range a.Outgoing {
		if o.DraftID == id && (o.State == "sending" || o.State == "uncertain" || o.State == "accepted") {
			return true
		}
	}
	return false
}
func (m *model) requestDelete() {
	if m.compose != nil || m.focus == treePane {
		m.notice = "Select a message or saved draft first."
		return
	}
	t := m.current()
	if t == nil {
		m.notice = "No message selected."
		return
	}
	if t.QueueID != "" {
		m.notice = "This move is already queued · R retries · Enter shows details"
		return
	}
	d := &deleteDialog{account: t.Account, threadID: t.ID, folder: t.Folder, subject: t.Subject, draft: isDraftThread(t)}
	if m.realAccount(t.Account) {
		d.key = m.liveKey(t.Account)
		a := m.live.disk.Accounts[d.key]
		if d.draft {
			if draftDeliveryPending(a, t.ID) {
				m.notice = "Resolve this draft's pending or uncertain delivery before deleting it."
				return
			}
		} else {
			d.destination = trashFolder(a)
			if d.destination == "" {
				m.notice = "No Trash folder identified. R syncs account folders; mail has been kept."
				return
			}
			if t.Folder == d.destination {
				m.notice = "Already in Trash. Permanent deletion is not available."
				return
			}
			pending := map[mailRef]bool{}
			for _, op := range a.Trash {
				for _, r := range op.Refs {
					pending[r] = true
				}
			}
			for _, r := range t.Refs {
				if r.Folder == t.Folder && r.UID != 0 && !pending[r] {
					d.refs = append(d.refs, r)
				}
			}
			if len(d.refs) == 0 {
				m.notice = "Move already pending, or mail is still awaiting its server UID. R refreshes."
				return
			}
		}
	} else {
		d.destination = "Trash"
		if t.Folder == d.destination {
			m.notice = "Already in Trash. Permanent deletion is not available."
			return
		}
	}
	m.deletion = d
	m.prefix = ""
}
func (m *model) confirmDelete() {
	d := m.deletion
	if d == nil {
		return
	}
	if d.key != "" {
		if m.liveKey(d.account) != d.key {
			m.deletion = nil
			m.notice = "Account changed; select the message again."
			return
		}
		a := copyAccount(m.live.disk.Accounts[d.key])
		if d.draft {
			if draftDeliveryPending(a, d.threadID) {
				m.deletion = nil
				m.notice = "Resolve delivery before deleting this draft."
				return
			}
			kept := a.Drafts[:0]
			for _, draft := range a.Drafts {
				if draft.ID != d.threadID {
					kept = append(kept, draft)
				}
			}
			a.Drafts = kept
		} else {
			a.Trash = append(a.Trash, trashChange{ID: opaqueID(), Destination: d.destination, Refs: append([]mailRef{}, d.refs...), Subject: d.subject, Created: m.now})
			// A real Sent UID proves the copy exists. Retire its local filing
			// receipt before queuing removal, so a concurrent Sent-copy retry
			// cannot recreate the message after we move it to Trash.
			for _, f := range a.Folders {
				if f.Name != d.folder || f.Role != imap.SentAttr {
					continue
				}
				for _, r := range a.Messages {
					if !refsOverlap([]mailRef{r.Ref}, d.refs) {
						continue
					}
					for i := range a.Outgoing {
						o := &a.Outgoing[i]
						if o.ID == r.MessageID && (o.State == "accepted" || o.State == "sent") {
							o.State, o.Wire = "filed", nil
						}
					}
				}
			}
		}
		if !m.commitMail(d.key, a) {
			return
		}
		m.deletion = nil
		m.reading = false
		m.focus = listPane
		m.scroll = 0
		m.rebuildLive()
		if d.draft {
			m.notice = "Draft deleted from this device."
		} else {
			m.queueSync(d.account, syncRequest{Folder: d.folder})
			m.notice = "Removed from this folder · Q shows pending Trash moves"
			if m.live.offline {
				m.notice = "Removed from this folder · queued offline · Q shows the queue"
			}
		}
	} else {
		for i := range m.threads {
			if m.threads[i].Account == d.account && m.threads[i].ID == d.threadID {
				if d.draft {
					m.threads = append(m.threads[:i], m.threads[i+1:]...)
				} else {
					m.threads[i].Folder = d.destination
				}
				break
			}
		}
		if !d.draft && !stringIn(m.accounts[d.account].Folders, d.destination) {
			m.accounts[d.account].Folders = append(m.accounts[d.account].Folders, d.destination)
		}
		m.deletion = nil
		m.reading = false
		m.focus = listPane
		m.scroll = 0
		m.normalizeSelection()
		m.notice = "Moved to Trash · demo changes last for this session"
		if d.draft {
			m.notice = "Demo draft deleted."
		}
	}
}

// An uncertain COPY never removes the source. Clearing this attempt is local;
// a subsequent dd is a fresh, explicitly confirmed move (and may copy again).
func (m *model) cancelUncertainDelete() {
	if !m.realAccount(m.account) {
		return
	}
	key := m.liveKey(m.account)
	if m.live.busy[key] {
		m.notice = "Wait for the current sync, then try :canceldelete again."
		return
	}
	t := m.current()
	if t == nil {
		return
	}
	a := copyAccount(m.live.disk.Accounts[key])
	kept := a.Trash[:0]
	removed := false
	for _, op := range a.Trash {
		if (op.State == "uncertain" || op.State == "copying") && refsOverlap(t.Refs, op.Refs) {
			removed = true
		} else {
			kept = append(kept, op)
		}
	}
	if !removed {
		m.notice = "No uncertain Trash copy on this conversation."
		return
	}
	a.Trash = kept
	if m.commitMail(key, a) {
		m.reading, m.scroll, m.focus = false, 0, listPane
		m.rebuildLive()
		m.notice = "Move cleared; original mail is visible again. Check Trash before another dd; a copy may exist."
	}
}
func (m model) handleDelete(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyPressMsg:
		switch msg.String() {
		case "esc", "q", "ctrl+c":
			m.deletion = nil
		case "tab", "shift+tab":
			m.deletion.selected = !m.deletion.selected
		case "h", "k", "left", "up":
			m.deletion.selected = false
		case "l", "j", "right", "down":
			m.deletion.selected = true
		case "enter":
			if m.deletion.selected {
				m.confirmDelete()
			} else {
				m.deletion = nil
			}
		}
	case tea.MouseClickMsg:
		if msg.Button != tea.MouseLeft {
			break
		}
		x, y, _, _ := m.deleteGeometry()
		bx, by := m.deleteButtons()
		if msg.Y == y+by {
			if msg.X >= x+bx && msg.X < x+bx+10 {
				m.deletion = nil
			}
			if msg.X >= x+bx+12 && msg.X < x+bx+28 {
				m.confirmDelete()
			}
		}
	}
	return m, nil
}
func (m model) deleteGeometry() (x, y, w, h int) {
	w = max(1, min(66, m.width-4))
	h = max(1, min(13, m.height))
	return max(0, (m.width-w)/2), max(0, (m.height-h)/2), w, h
}
func (m model) deleteButtons() (x, y int) {
	_, _, w, h := m.deleteGeometry()
	return max(1, (w-28)/2), max(1, h-5)
}
func (m model) overlayDelete(lines []string) []string {
	x, y, w, h := m.deleteGeometry()
	inner := max(0, w-2)
	d := m.deletion
	rows := make([]string, h)
	for i := range rows {
		rows[i] = styled("│", violet) + strings.Repeat(" ", inner) + styled("│", violet)
	}
	put := func(y int, s string) {
		if y > 0 && y < h-1 {
			rows[y] = styled("│", violet) + fit(s, inner) + styled("│", violet)
		}
	}
	title, action := "Move to Trash?", "Move to Trash"
	description := fmt.Sprintf("%d cached message(s) from %s", len(d.refs), d.folder)
	detail := "Other folders and saved replies stay intact."
	if d.key == "" {
		description = "Selected conversation from " + d.folder
	}
	if d.draft {
		title, action = "Delete saved draft?", "Delete draft"
		description = "Remove this draft from this device."
		detail = "This cannot be undone. No mail will be sent."
	}
	put(1, center(bold(title, cyan), inner))
	put(3, center(styled(safeMailText(d.subject), readerText), inner))
	put(4, center(styled(description, oceanText), inner))
	for i, line := range strings.Split(ansi.Wrap(detail, max(1, inner-4), ""), "\n") {
		if i < 2 {
			put(5+i, center(styled(line, muted), inner))
		}
	}
	bx, by := m.deleteButtons()
	button := func(label string, width int, selected bool) string {
		st := lip.NewStyle().Foreground(cyan).Background(bluePane)
		if selected {
			st = st.Foreground(ink).Background(cyan).Bold(true)
		}
		return st.Render(center(label, width))
	}
	put(by, strings.Repeat(" ", max(0, bx-1))+button("Cancel", 10, !d.selected)+"  "+button(action, 16, d.selected))
	put(h-3, center(styled("h/l · j/k · Tab select · Enter confirm", muted), inner))
	put(h-2, center(styled("Esc cancels", oceanText), inner))
	rows[0] = gradient("╭"+strings.Repeat("─", inner)+"╮", 0)
	if h > 1 {
		rows[h-1] = gradient("╰"+strings.Repeat("─", inner)+"╯", 0)
	}
	for i, row := range rows {
		if y+i >= len(lines) {
			break
		}
		row = onSurface(fit(row, w), violetPane)
		lines[y+i] = ansi.Cut(lines[y+i], 0, x) + row + ansi.Cut(lines[y+i], x+w, m.width)
	}
	return lines
}
