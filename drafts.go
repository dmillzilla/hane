package main

// Saved replies live in the account's Drafts folder until sent. Resolve them
// by conversation as well as draft ID so replying from the inbox resumes work.
func (m model) savedReply(account, threadID int) *Thread {
	for i := range m.threads {
		t := &m.threads[i]
		if t.Account == account && isDraftThread(t) && (t.ReplyTo == threadID || m.draftMatchesThread(t, threadID)) {
			return t
		}
	}
	return nil
}

func (m model) conversationLabel(t *Thread) string {
	if isDraftThread(t) {
		return "Draft · Enter to resume"
	}
	label := threadLabel(t)
	if m.savedReply(t.Account, t.ID) != nil {
		label = "Draft · r to resume · " + label
	}
	return label
}
