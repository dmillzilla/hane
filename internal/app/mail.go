package app

import (
	"fmt"
	"sort"
	"strings"
	"time"
)

// The UI consumes this model independently of transport. A future provider can
// populate it without putting IMAP, SMTP, or OAuth details in the views.
type Account struct {
	Name, Address    string
	Folders          []string
	Config           *AccountConfig
	Verified         bool
	ConnectionStatus string
}
type Message struct {
	Sender, Address, Body string
	Received              time.Time
}
type Thread struct {
	QueueID            string
	Remote, LocalDraft bool
	Refs               []mailRef
	RemoteIDs          []string
	ReplyID            string
	References         []string
	ID, Account        int
	Folder, Subject    string
	Unread, Starred    bool
	Messages           []Message
	ReplyTo            int
	Recipient          string
}

func (t *Thread) newest() Message {
	if len(t.Messages) == 0 {
		return Message{}
	}
	return t.Messages[0]
}
func normalize(threads []Thread) {
	for i := range threads {
		sort.SliceStable(threads[i].Messages, func(a, b int) bool {
			return threads[i].Messages[a].Received.After(threads[i].Messages[b].Received)
		})
	}
	sort.SliceStable(threads, func(i, j int) bool { return threads[i].newest().Received.After(threads[j].newest().Received) })
}

func filtered(threads []Thread, account int, folder, query string) []*Thread {
	out := []*Thread{}
	query = strings.ToLower(query)
	for i := range threads {
		t := &threads[i]
		if t.Account != account || t.Folder != folder {
			continue
		}
		match := query == "" || strings.Contains(strings.ToLower(t.Subject), query)
		if !match {
			for _, msg := range t.Messages {
				if strings.Contains(strings.ToLower(msg.Sender+" "+msg.Address+" "+msg.Body), query) {
					match = true
					break
				}
			}
		}
		if match {
			out = append(out, t)
		}
	}
	return out
}

func seed(now time.Time) ([]Account, []Thread) {
	accounts := []Account{
		{Name: "Personal", Address: "me@example.com", Folders: []string{"Inbox", "Starred", "Sent", "Drafts", "Archive", "Travel"}},
		{Name: "Studio", Address: "studio@example.com", Folders: []string{"Inbox", "Starred", "Sent", "Drafts", "Archive", "Projects"}},
	}
	items := []struct{ sender, subject, body string }{
		{"Aiko Tanaka", "A slower route through Kyoto", "Let's take the early train and walk along the river before the city wakes up. I found a small coffee shop near the bridge that opens at seven.\n\nThe revised plan looks like this:\n\nFriday — arrive, drop our bags, and explore the little streets around Higashiyama.\nSaturday — an early walk along the Philosopher's Path, then the bookshop you mentioned.\nSunday — no schedule. Just coffee, a notebook, and whatever we stumble into.\n\nI've left the afternoons open so we can stay longer wherever we like. The last trip felt a little too rushed, and I'd love to leave room for surprises this time.\n\nDoes the early train still work for you?\n\nAiko"},
		{"Ren Ito", "The ocean palette, version two", "I pushed the new color studies. The transition from deep teal into violet feels much calmer now, especially against the ink background.\n\nI kept the main reading text neutral and used color only where it helps you find your place: the active pane, unread markers, and the account you're replying from.\n\nTry it in a narrow terminal too. The gradient should stretch with the window, and the content should have enough breathing room without feeling empty.\n\nLet me know which direction feels right.\n\nRen"},
		{"Maya Chen", "Saturday's listening session", "Bringing a few records over on Saturday. The new jazz pressing finally arrived, and the sleeve artwork is even better in person.\n\nHow does six sound? I'll pick up dinner on the way. If the weather holds, we can start out on the patio.\n\nMaya"},
		{"Kissa Journal", "Field notes / small spaces, good coffee", "This week's letter is about places that make you want to stay a little longer. A corner table, the sound of a kettle, and light moving across a wooden floor.\n\nWe spoke with three independent cafe owners about the details that matter: a welcoming doorway, a thoughtful playlist, and a menu short enough to remember.\n\nRead slowly. Brew something good.\n\nThe Kissa team"},
		{"Jonah Park", "A few photos from the coast", "I finally sorted through the photos from our coastal walk. The fog lifted just as we reached the lighthouse.\n\nMy favorite is the one of the fishing boats, where the water almost blends into the sky. I'll bring a few prints next time.\n\nThere are more trails along that stretch if we want to go back in the fall.\n\nJonah"},
		{"Emi Sato", "Book club: next month's pick", "The votes are in. We're reading a collection of short stories next month, which should leave plenty of room for discussion.\n\nLet's meet at the usual spot on the last Thursday. Bring a passage you liked, a question, or just yourself.\n\nI'll reserve the big table.\n\nEmi"},
		{"Leo Martins", "Keyboard build night", "The switches arrived. I have enough for both boards, plus a few extras if you want to compare the lighter springs.\n\nBring your keycaps and a playlist. I'll clear the desk and make some room for the soldering station.\n\nLeo"},
		{"Nora Williams", "Sunday market finds", "Found a stall selling handmade paper and thought of you. Some of the notebooks have stitched bindings and beautifully uneven edges.\n\nThe market is open again this Sunday. We could grab breakfast and walk through before it gets busy.\n\nNora"},
	}
	var threads []Thread
	id := 1
	for a := range accounts {
		for f, folder := range accounts[a].Folders {
			if folder == "Drafts" || folder == "Starred" {
				continue
			}
			count := 3
			if folder == "Inbox" {
				count = 8
			}
			for i := 0; i < count; i++ {
				item := items[(i+a*2+f)%len(items)]
				received := now.Add(-time.Duration(8+i*47+f*1440+a*16) * time.Minute)
				subject := item.subject
				if a == 1 && f == 0 && i == 0 {
					subject = "Prototype review / navigation and rhythm"
				}
				msg := Message{item.sender, strings.ToLower(strings.ReplaceAll(item.sender, " ", ".")) + "@example.com", item.body, received}
				messages := []Message{msg}
				if i%2 == 0 {
					messages = append(messages,
						Message{"You", accounts[a].Address, "Thanks for putting this together. I like the direction, especially the small details.\n\nCould you share the updated plan when you have a moment? I'd love to make sure we leave enough time to enjoy it.\n\nDaniel", received.Add(-3 * time.Hour)},
						Message{item.sender, msg.Address, "Hey Daniel,\n\nI've been thinking about our conversation and started gathering a few ideas. There is something nice about giving ourselves a little more space to explore.\n\nI'll send a more detailed update tomorrow. In the meantime, tell me if there's anything you'd like to include.\n\nTalk soon,\n" + strings.Fields(item.sender)[0], received.Add(-26 * time.Hour)})
				}
				if folder == "Sent" {
					messages[0].Sender = "You"
					messages[0].Address = accounts[a].Address
				}
				threads = append(threads, Thread{ID: id, Account: a, Folder: folder, Subject: subject, Unread: folder == "Inbox" && i < 3, Starred: i%3 == 0, Messages: messages})
				id++
			}
		}
	}
	normalize(threads)
	return accounts, threads
}

func stamp(t time.Time, now time.Time) string {
	t = t.In(now.Location())
	if t.Format("2006-01-02") == now.Format("2006-01-02") {
		return t.Format("15:04")
	}
	return t.Format("Jan 02")
}

func (m model) folderThreads() []*Thread {
	if m.folder != "Starred" {
		return filtered(m.threads, m.account, m.folder, m.query)
	}
	var out []*Thread
	for _, folder := range m.accounts[m.account].Folders {
		if folder == "Starred" {
			continue
		}
		for _, t := range filtered(m.threads, m.account, folder, m.query) {
			if t.Starred {
				out = append(out, t)
			}
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].newest().Received.After(out[j].newest().Received) })
	return out
}

func (m model) count(account int, folder string) int {
	n := 0
	for _, t := range m.threads {
		if t.Account == account && ((folder == "Starred" && t.Starred) || (folder != "Starred" && t.Folder == folder)) {
			n++
		}
	}
	return n
}

func countUnread(threads []*Thread) int {
	n := 0
	for _, t := range threads {
		if t.Unread {
			n++
		}
	}
	return n
}

// Explorer counts cover the whole folder, independently of the active search.
func (m model) unreadCount(account int, folder string) int {
	m.account, m.folder, m.query = account, folder, ""
	return countUnread(m.folderThreads())
}

func threadLabel(t *Thread) string {
	if len(t.Messages) == 1 {
		return "1 message"
	}
	return fmt.Sprintf("%d messages", len(t.Messages))
}
