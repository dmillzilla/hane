package app

import (
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"
)

func TestUnreadCountsFollowReadingAndStarredViews(t *testing.T) {
	m := newModel(time.Now())
	if m.unreadCount(0, "Inbox") != 3 || m.unreadCount(1, "Inbox") != 3 || m.unreadCount(0, "Starred") != 1 {
		t.Fatal("counts must include only unread conversations in each account and folder")
	}
	m = press(m, "l")
	if m.unreadCount(0, "Inbox") != 2 || m.unreadCount(0, "Starred") != 0 || m.unreadCount(1, "Inbox") != 3 {
		t.Fatal("opening a conversation must update Inbox and Starred only in its account")
	}
	m = press(m, "hu")
	if m.unreadCount(0, "Inbox") != 3 || m.unreadCount(0, "Starred") != 1 {
		t.Fatal("marking unread must restore both counts")
	}
	m = press(m, "s")
	if m.unreadCount(0, "Starred") != 0 || m.unreadCount(0, "Inbox") != 3 {
		t.Fatal("un-starring must update only the Starred count")
	}
}

func TestUnreadHeaderUsesSearchButExplorerUsesWholeFolder(t *testing.T) {
	m := newModel(time.Now())
	m.query = "Kyoto"
	if countUnread(m.folderThreads()) != 1 || m.unreadCount(0, "Inbox") != 3 {
		t.Fatal("search must narrow the header count without changing explorer counts")
	}
	if !strings.Contains(ansi.Strip(m.listView(54)), "1 unread") || !strings.Contains(ansi.Strip(m.treeView(29)), "Inbox       3") {
		t.Fatal("views must display their unread counts")
	}
	m = press(m, "l")
	if !strings.Contains(ansi.Strip(m.listView(54)), "0 unread") || m.unreadCount(0, "Inbox") != 2 {
		t.Fatal("read filtered conversation must show zero unread without hiding the conversation")
	}
	if len(m.folderThreads()) != 1 {
		t.Fatal("read conversations must remain navigable")
	}
	m.query, m.folder = "", "Sent"
	if m.count(0, "Sent") == 0 || m.unreadCount(0, "Sent") != 0 {
		t.Fatal("a nonempty folder containing read mail must show zero unread")
	}
}
