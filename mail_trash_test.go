package main

import (
	"bufio"
	"context"
	"crypto/tls"
	"fmt"
	"net"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/emersion/go-imap"
	"github.com/emersion/go-imap/client"
)

type trashWireState struct {
	sync.Mutex
	boxes                 map[string]map[uint32]bool // UID -> Deleted flag
	commands              []string
	validity              uint32
	loseCopy, loseExpunge bool
}

func trashWireFixture(t *testing.T, caps string) (imapTransport, AccountConfig, *trashWireState) {
	t.Helper()
	cert, roots := testCertificate(t)
	ln, err := tls.Listen("tcp", "127.0.0.1:0", &tls.Config{Certificates: []tls.Certificate{cert}})
	if err != nil {
		t.Fatal(err)
	}
	state := &trashWireState{boxes: map[string]map[uint32]bool{"INBOX": {42: false, 99: true}, "Trash": {}}, validity: 7}
	var wg sync.WaitGroup
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			wg.Add(1)
			go func() {
				defer wg.Done()
				defer conn.Close()
				conn.SetDeadline(time.Now().Add(10 * time.Second))
				reader := bufio.NewScanner(conn)
				fmt.Fprint(conn, "* OK test mail\r\n")
				selected := "INBOX"
				for reader.Scan() {
					line := reader.Text()
					fields := strings.Fields(line)
					if len(fields) < 2 {
						return
					}
					tag := fields[0]
					cmd := strings.ToUpper(fields[1])
					state.Lock()
					state.commands = append(state.commands, strings.Join(fields[1:], " "))
					reply := ""
					drop := false
					switch cmd {
					case "CAPABILITY":
						reply = "* CAPABILITY IMAP4rev1 " + caps + "\r\n"
					case "LOGIN":
					case "LIST":
						reply = "* LIST () \"/\" INBOX\r\n* LIST (\\Trash) \"/\" Trash\r\n"
					case "STATUS":
						name := strings.Trim(fields[2], "\"")
						validity := uint32(9)
						if name == "INBOX" {
							validity = state.validity
						}
						reply = fmt.Sprintf("* STATUS %s (MESSAGES %d UNSEEN 0 UIDVALIDITY %d)\r\n", name, len(state.boxes[name]), validity)
					case "SELECT", "EXAMINE":
						selected = strings.Trim(fields[2], "\"")
						v := uint32(9)
						if selected == "INBOX" {
							v = state.validity
						}
						reply = fmt.Sprintf("* FLAGS (\\Seen \\Deleted)\r\n* %d EXISTS\r\n* OK [UIDVALIDITY %d] valid\r\n", len(state.boxes[selected]), v)
					case "UID":
						sub := strings.ToUpper(fields[2])
						setIndex := 3
						setText := fields[setIndex]
						if sub == "SEARCH" {
							setText = "1:*"
							for i := 3; i < len(fields)-1; i++ {
								if strings.EqualFold(fields[i], "UID") {
									setIndex = i + 1
									setText = fields[setIndex]
									break
								}
							}
						}
						set, e := imap.ParseSeqSet(setText)
						if e != nil {
							reply = "bad"
							break
						}
						switch sub {
						case "FETCH":
							seq := 0
							for id, deleted := range state.boxes[selected] {
								seq++
								if !set.Contains(id) {
									continue
								}
								flags := ""
								if deleted {
									flags = imap.DeletedFlag
								}
								reply += fmt.Sprintf("* %d FETCH (UID %d FLAGS (%s))\r\n", seq, id, flags)
							}
						case "SEARCH":
							reply = "* SEARCH"
							for id, deleted := range state.boxes[selected] {
								if set.Contains(id) && (!strings.Contains(strings.ToUpper(line), "UNDELETED") || !deleted) {
									reply += " " + strconv.Itoa(int(id))
								}
							}
							reply += "\r\n"
						case "MOVE", "COPY":
							destination := strings.Trim(fields[4], "\"")
							src, dst := new(imap.SeqSet), new(imap.SeqSet)
							for id, deleted := range state.boxes[selected] {
								if set.Contains(id) {
									next := uint32(101)
									for n := range state.boxes[destination] {
										if n >= next {
											next = n + 1
										}
									}
									state.boxes[destination][next] = deleted
									src.AddNum(id)
									dst.AddNum(next)
									if sub == "MOVE" {
										delete(state.boxes[selected], id)
									}
								}
							}
							reply = fmt.Sprintf("%s OK [COPYUID 9 %s %s] copied\r\n", tag, src.String(), dst.String())
							if sub == "COPY" && state.loseCopy {
								state.loseCopy = false
								drop = true
							}
						case "STORE":
							for id := range state.boxes[selected] {
								if set.Contains(id) {
									state.boxes[selected][id] = true
								}
							}
						case "EXPUNGE":
							for id, deleted := range state.boxes[selected] {
								if set.Contains(id) && deleted {
									delete(state.boxes[selected], id)
								}
							}
							if state.loseExpunge {
								state.loseExpunge = false
								drop = true
							}
						default:
							reply = "bad"
						}
					default:
						reply = "bad"
					}
					state.Unlock()
					if drop {
						return
					}
					if reply == "bad" {
						fmt.Fprintf(conn, "%s BAD unsupported\r\n", tag)
					} else if strings.HasPrefix(reply, tag+" ") {
						fmt.Fprint(conn, reply)
					} else {
						fmt.Fprint(conn, reply)
						fmt.Fprintf(conn, "%s OK done\r\n", tag)
					}
				}
			}()
		}
	}()
	t.Cleanup(func() { ln.Close(); <-done; wg.Wait() })
	c := credentialConfig(t)
	c.IMAPHost = "127.0.0.1"
	c.IMAPPort = ln.Addr().(*net.TCPAddr).Port
	return imapTransport{roots: roots}, c, state
}
func connectTrashFixture(t *testing.T, b imapTransport, c AccountConfig) *client.Client {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	im, close, err := b.connect(ctx, c, "password")
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	t.Cleanup(func() { close(); cancel() })
	return im
}
func trashFixtureFolders() []folderInfo {
	return []folderInfo{{Name: "INBOX", Validity: 7}, {Name: "Trash", Role: imap.TrashAttr, Validity: 9}}
}
func trashFixtureOp() trashChange {
	return trashChange{ID: "move", Destination: "Trash", Refs: []mailRef{{"INBOX", 7, 42}}}
}
func TestNativeTrashMoveIsScopedAndRetrySafe(t *testing.T) {
	b, c, s := trashWireFixture(t, "MOVE UIDPLUS")
	im := connectTrashFixture(t, b, c)
	for i := 0; i < 2; i++ {
		_, done, stale, err := moveToTrash(im, trashFixtureFolders(), trashFixtureOp())
		if err != nil || !done || stale {
			t.Fatalf("move result: %t %t %v", done, stale, err)
		}
	}
	s.Lock()
	defer s.Unlock()
	if len(s.boxes["Trash"]) != 1 || len(s.boxes["INBOX"]) != 1 || !s.boxes["INBOX"][99] {
		t.Fatal("move/retry touched unrelated Deleted mail or duplicated Trash")
	}
	for _, cmd := range s.commands {
		if strings.Contains(cmd, "EXPUNGE") || strings.Contains(cmd, "COPY") {
			t.Fatal("native move must not run fallback")
		}
	}
}
func TestUIDPlusTrashRequiresCopyReceiptBeforeScopedExpunge(t *testing.T) {
	b, c, s := trashWireFixture(t, "UIDPLUS")
	im := connectTrashFixture(t, b, c)
	op := trashFixtureOp()
	ready, done, _, err := moveToTrash(im, trashFixtureFolders(), op)
	if err != nil || done || ready.State != "ready" {
		t.Fatal("first phase must only prepare COPY", err)
	}
	copied, done, _, err := moveToTrash(im, trashFixtureFolders(), ready)
	if err != nil || done || copied.State != "copied" || len(copied.Copies) != 1 {
		t.Fatal("COPY must return receipt before removing source", err)
	}
	s.Lock()
	_, present := s.boxes["INBOX"][42]
	s.Unlock()
	if !present {
		t.Fatal("source removed before receipt persisted")
	}
	_, done, _, err = moveToTrash(im, trashFixtureFolders(), copied)
	if err != nil || !done {
		t.Fatal("finalize failed", err)
	}
	_, done, _, err = moveToTrash(im, trashFixtureFolders(), copied)
	if err != nil || !done {
		t.Fatal("retry failed", err)
	}
	s.Lock()
	defer s.Unlock()
	if len(s.boxes["Trash"]) != 1 || !s.boxes["INBOX"][99] {
		t.Fatal("targeted expunge affected unrelated mail")
	}
	for _, cmd := range s.commands {
		if strings.HasPrefix(cmd, "EXPUNGE") || strings.HasPrefix(cmd, "UID MOVE") {
			t.Fatal("unsafe fallback command")
		}
	}
}
func TestTrashFailuresKeepOriginals(t *testing.T) {
	for _, mode := range []string{"unsupported", "stale", "missing-copy", "lost-copy"} {
		t.Run(mode, func(t *testing.T) {
			caps := "UIDPLUS"
			if mode == "unsupported" {
				caps = ""
			}
			b, c, s := trashWireFixture(t, caps)
			im := connectTrashFixture(t, b, c)
			op := trashFixtureOp()
			switch mode {
			case "stale":
				op.Refs[0].Validity = 123
			case "missing-copy":
				op.State = "copied"
				op.Copies = []mailRef{{"Trash", 9, 555}}
			case "lost-copy":
				op.State = "ready"
				s.Lock()
				s.loseCopy = true
				s.Unlock()
			}
			updated, done, stale, err := moveToTrash(im, trashFixtureFolders(), op)
			if mode == "stale" {
				if !done || !stale || err != nil {
					t.Fatal("stale intent must be dropped safely")
				}
			} else if err == nil || done {
				t.Fatal("unsafe operation should fail")
			}
			if mode == "lost-copy" {
				if updated.State != "uncertain" {
					t.Fatal("lost COPY reply must be uncertain")
				}
				_, done, _, err = moveToTrash(nil, nil, updated)
				if done || err != nil {
					t.Fatal("uncertain COPY must not issue commands")
				}
			}
			s.Lock()
			defer s.Unlock()
			if _, ok := s.boxes["INBOX"][42]; !ok {
				t.Fatal("failure removed original")
			}
			for _, cmd := range s.commands {
				if strings.Contains(cmd, "STORE") || strings.Contains(cmd, "EXPUNGE") {
					t.Fatal("failure must not mark/delete originals")
				}
			}
		})
	}
}
func TestTrashLostExpungeReplyResumesWithoutCopyingAgain(t *testing.T) {
	b, c, s := trashWireFixture(t, "UIDPLUS")
	im := connectTrashFixture(t, b, c)
	op := trashFixtureOp()
	op.State = "ready"
	copied, _, _, err := moveToTrash(im, trashFixtureFolders(), op)
	if err != nil {
		t.Fatal(err)
	}
	s.Lock()
	s.loseExpunge = true
	s.Unlock()
	updated, done, _, err := moveToTrash(im, trashFixtureFolders(), copied)
	if err == nil || done || updated.State != "copied" {
		t.Fatal("lost final response must preserve receipt")
	}
	im = connectTrashFixture(t, b, c)
	_, done, _, err = moveToTrash(im, trashFixtureFolders(), updated)
	if err != nil || !done {
		t.Fatal("retry should detect missing source UID", err)
	}
	s.Lock()
	defer s.Unlock()
	if len(s.boxes["Trash"]) != 1 || !s.boxes["INBOX"][99] {
		t.Fatal("retry duplicated or removed unrelated mail")
	}
}

func TestTrashSyncPersistsEachPhaseBeforeRemovingOriginal(t *testing.T) {
	for _, failReceiptSave := range []bool{false, true} {
		t.Run(fmt.Sprintf("receipt_save_fails=%t", failReceiptSave), func(t *testing.T) {
			b, c, s := trashWireFixture(t, "UIDPLUS")
			m := newModel(time.Now())
			m.appendConfiguredAccount(c, false)
			if err := m.initLive(t.TempDir() + "/mail.json"); err != nil {
				t.Fatal(err)
			}
			key := m.liveKey(0)
			m.live.backend = b
			m.live.passwords[key] = "password"
			a := accountMail{Folders: trashFixtureFolders(), Messages: []remoteMail{{Ref: mailRef{"INBOX", 7, 42}, MessageID: "<test@example.com>", Subject: "Test mail", Body: "Fictional body", BodyLoaded: true, Received: time.Now()}}, Trash: []trashChange{trashFixtureOp()}}
			if !m.commitMail(key, a) {
				t.Fatal(m.notice)
			}
			m.rebuildLive()
			m.queueSync(0, syncRequest{Folder: "INBOX"})
			for step := 0; step < 3; step++ {
				if len(m.live.pending) != 1 {
					t.Fatalf("expected next sync at phase %d, got %d: %s", step, len(m.live.pending), m.notice)
				}
				cmd := m.live.pending[0]
				m.live.pending = nil
				disk, err := loadMailDisk(m.live.path)
				if err != nil {
					t.Fatal(err)
				}
				if step == 1 && disk.Accounts[key].Trash[0].State != "uncertain" {
					t.Fatal("disk must already record a potentially interrupted COPY before the worker executes")
				}
				if step == 2 && disk.Accounts[key].Trash[0].State != "copied" {
					t.Fatal("copy receipt must be durable before removing originals")
				}
				result := cmd().(liveSyncMsg)
				if result.Result.Err != nil {
					t.Fatal(result.Result.Err)
				}
				if step == 1 && failReceiptSave {
					m.live.path += "/cannot-save.json"
					m.finishLiveSync(result)
					if len(m.live.pending) != 0 {
						t.Fatal("failed receipt save must not start expunge phase")
					}
					s.Lock()
					defer s.Unlock()
					if _, exists := s.boxes["INBOX"][42]; !exists {
						t.Fatal("source lost without a saved receipt")
					}
					for _, cmd := range s.commands {
						if strings.Contains(cmd, "STORE") || strings.Contains(cmd, "EXPUNGE") {
							t.Fatal("disk failure triggered source deletion")
						}
					}
					return
				}
				m.finishLiveSync(result)
			}
			if len(m.live.disk.Accounts[key].Trash) != 0 || len(m.folderThreads()) != 0 {
				t.Fatal("completed move did not clear queue and inbox")
			}
			s.Lock()
			defer s.Unlock()
			if len(s.boxes["Trash"]) != 1 || !s.boxes["INBOX"][99] {
				t.Fatal("sync touched unrelated mail")
			}
		})
	}
}
