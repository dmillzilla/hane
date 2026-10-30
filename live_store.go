package main

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"time"
)

const localDraftFolder = "Drafts (local)"
const syncQueueFolder = "Sync queue"

type mailRef struct {
	Folder        string
	Validity, UID uint32
}

func (r mailRef) key() string { b, _ := json.Marshal(r); return string(b) }

type remoteMail struct {
	Ref                                           mailRef
	MessageID, InReplyTo                          string
	References                                    []string
	Subject, Sender, From, To, ReplyAddress, Body string
	Received                                      time.Time
	Seen, Starred, BodyLoaded                     bool
	Attachments                                   []string
}
type folderInfo struct {
	Name, Role              string
	Total, Unread, Validity uint32
	Loaded                  int
	Complete                bool
}
type flagChange struct {
	ID    string
	Ref   mailRef
	Flag  string
	Value bool
}

// Each move is bound to the exact source UIDs confirmed by the user. Retrying
// after a lost response never targets new arrivals or newly assigned UIDs.
type trashChange struct {
	ID, Destination string
	Refs            []mailRef
	State           string // pending, ready, copying, copied, uncertain
	Copies          []mailRef
	Subject, Error  string
	Created         time.Time
}
type storedDraft struct {
	ID                         int
	ReplyID, To, Subject, Body string
	References                 []string
	Updated                    time.Time
}
type outgoingMail struct {
	ID                         string
	DraftID                    int
	To, Subject, Body, ReplyID string
	References                 []string
	Wire                       []byte
	Date                       time.Time
	State, Error               string
}
type accountMail struct {
	Folders  []folderInfo
	Messages []remoteMail
	Drafts   []storedDraft
	Flags    []flagChange
	Trash    []trashChange
	Outgoing []outgoingMail
	Synced   time.Time
}
type mailDisk struct {
	Version  int
	Accounts map[string]accountMail
}

func mailDataPath() (string, error) {
	dir := os.Getenv("XDG_DATA_HOME")
	if dir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		dir = filepath.Join(home, ".local", "share")
	}
	return filepath.Join(dir, storageNamespace, "mail.json"), nil
}
func loadMailDisk(path string) (mailDisk, error) {
	d := mailDisk{Version: 1, Accounts: map[string]accountMail{}}
	f, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return d, nil
	}
	if err != nil {
		return d, err
	}
	defer f.Close()
	dec := json.NewDecoder(io.LimitReader(f, 512<<20))
	if err = dec.Decode(&d); err != nil {
		return d, err
	}
	var extra any
	if err = dec.Decode(&extra); err != io.EOF {
		return d, errors.New("invalid mail cache suffix")
	}
	if d.Version != 1 || d.Accounts == nil {
		return d, errors.New("unsupported mail cache")
	}
	for key, a := range d.Accounts {
		for i := range a.Trash {
			if a.Trash[i].State == "copying" {
				a.Trash[i].State = "uncertain"
			}
		}
		for i := range a.Outgoing {
			if a.Outgoing[i].State == "sending" {
				a.Outgoing[i].State = "uncertain"
				a.Outgoing[i].Error = "The app stopped during delivery. Check Sent before retrying."
			}
		}
		d.Accounts[key] = a
	}
	return d, nil
}
func saveMailDisk(path string, d mailDisk) error {
	if path == "" {
		return errors.New("mail storage is unavailable")
	}
	return atomicPrivateJSON(path, d)
}
func atomicPrivateJSON(path string, v any) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	f, err := os.CreateTemp(dir, ".mail-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if err = f.Chmod(0600); err == nil {
		err = json.NewEncoder(f).Encode(v)
	}
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	if err = os.Rename(f.Name(), path); err != nil {
		return err
	}
	// Sync the directory too so the rename survives a power failure.
	df, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer df.Close()
	return df.Sync()
}
func copyAccount(a accountMail) accountMail {
	b, _ := json.Marshal(a)
	var c accountMail
	_ = json.Unmarshal(b, &c)
	return c
}
func opaqueID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic("system random source unavailable")
	}
	return hex.EncodeToString(b[:])
}
func stableMailID(s string) int {
	b := sha256.Sum256([]byte(s))
	return int(binary.BigEndian.Uint64(b[:8])&((1<<52)-1)) + 1000
}
func isDraftThread(t *Thread) bool { return t.LocalDraft || (t.Folder == "Drafts" && !t.Remote) }
