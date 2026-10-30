package app

import (
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/x/ansi"
)

func pendingTrashRefs(a accountMail) map[mailRef]bool {
	refs := map[mailRef]bool{}
	for _, op := range a.Trash {
		for _, ref := range op.Refs {
			refs[ref] = true
		}
	}
	return refs
}
func hiddenFolderCounts(a accountMail, folder string) (total, unread int) {
	hidden := pendingTrashRefs(a)
	for _, r := range a.Messages {
		if r.Ref.Folder == folder && hidden[r.Ref] {
			total++
			if !r.Seen {
				unread++
			}
		}
	}
	return
}
func (m *model) openSyncQueue() {
	if !m.realAccount(m.account) {
		m.notice = "The sync queue is available for configured accounts."
		return
	}
	m.folder = syncQueueFolder
	m.query = ""
	m.selected = 0
	m.listOffset = 0
	m.scroll = 0
	m.reading = false
	m.revealTree()
	m.focus = listPane
	m.notice = "Sync queue · Enter details · R retry"
}
func (m model) queueOperation(t *Thread) (trashChange, bool) {
	if t == nil || t.QueueID == "" || !m.realAccount(t.Account) {
		return trashChange{}, false
	}
	for _, op := range m.live.disk.Accounts[m.liveKey(t.Account)].Trash {
		if op.ID == t.QueueID {
			return op, true
		}
	}
	return trashChange{}, false
}
func (m model) trashStatus(index int, op trashChange) string {
	if op.State == "uncertain" || op.State == "copying" && !m.live.busy[m.liveKey(index)] {
		return "Needs review"
	}
	if m.live.offline {
		return "Waiting for connection"
	}
	if m.live.busy[m.liveKey(index)] {
		if op.State == "copied" {
			return "Finishing move"
		}
		if op.Error != "" {
			return "Retrying"
		}
		return "Syncing"
	}
	if op.Error != "" {
		return "Retry needed"
	}
	if op.State == "copied" {
		return "Waiting to finish"
	}
	return "Waiting to sync"
}
func (m model) trashQueueDetails(index int, op trashChange) string {
	source := "Source folder"
	if len(op.Refs) > 0 {
		source = op.Refs[0].Folder
	}
	body := fmt.Sprintf("%s\n\nMove %d message(s)\n%s → %s\n\nRemoved from your mail views while this move is pending.", m.trashStatus(index, op), len(op.Refs), source, op.Destination)
	if op.Error != "" {
		body += "\n\nLast sync error:\n" + safeMailText(op.Error)
	}
	if op.State == "uncertain" || op.State == "copying" && !m.live.busy[m.liveKey(index)] {
		body += "\n\nA Trash copy may exist. Originals have been kept on the server. Check Trash before retrying.\n\n:canceldelete clears this attempt and makes the originals visible again. Another dd may create a second Trash copy."
	} else if m.live.offline {
		body += "\n\nThis move is saved on this device. Restart online to sync."
	} else {
		body += "\n\nR retries synchronization. This entry disappears when the move completes."
	}
	return body
}
func (m model) trashQueueThreads(index int, a accountMail) []Thread {
	var out []Thread
	for _, op := range a.Trash {
		subject, created := op.Subject, op.Created
		for _, r := range a.Messages {
			if refsOverlap([]mailRef{r.Ref}, op.Refs) {
				if subject == "" {
					subject = r.Subject
				}
				if created.IsZero() {
					created = r.Received
				}
				break
			}
		}
		if subject == "" {
			subject = "Move to Trash"
		}
		if created.IsZero() {
			created = time.Unix(0, 0)
		}
		out = append(out, Thread{ID: stableMailID(m.liveKey(index) + "/trash/" + op.ID), QueueID: op.ID, Account: index, Folder: syncQueueFolder, Refs: append([]mailRef{}, op.Refs...), Subject: subject, Messages: []Message{{Sender: "Move to Trash", Body: m.trashQueueDetails(index, op), Received: created}}})
	}
	return out
}
func (m model) queueReaderLines(t *Thread, w int) []string {
	op, ok := m.queueOperation(t)
	if !ok {
		return nil
	}
	var lines []string
	for _, line := range strings.Split(ansi.Wrap(m.trashQueueDetails(t.Account, op), w, ""), "\n") {
		lines = append(lines, styled(line, readerText))
	}
	return lines
}
