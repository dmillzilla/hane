package main

import (
	"bufio"
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"log"
	"net"
	"net/textproto"
	"strings"
	"testing"
	"time"

	"github.com/emersion/go-imap"
	"github.com/emersion/go-imap/backend/memory"
	"github.com/emersion/go-imap/server"
)

func liveIMAPFixture(t *testing.T) (imapTransport, AccountConfig) {
	t.Helper()
	cert, roots := testCertificate(t)
	be := memory.New()
	user, err := be.Login(nil, "username", "password")
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"Archive", "Sent"} {
		if err = user.CreateMailbox(name); err != nil {
			t.Fatal(err)
		}
	}
	raws := map[string]string{
		"Archive": "From: Parent <parent@example.com>\r\nTo: reader@example.com\r\nSubject: Thread\r\nMessage-ID: <parent@example.com>\r\nContent-Type: text/plain; charset=utf-8\r\n\r\nThe older parent",
		"INBOX":   "From: Reply <reply@example.com>\r\nTo: reader@example.com\r\nReply-To: Replies <replies@example.com>\r\nSubject: Re: Thread\r\nMessage-ID: <child@example.com>\r\nReferences: <parent@example.com>\r\nIn-Reply-To: <parent@example.com>\r\nMIME-Version: 1.0\r\nContent-Type: multipart/alternative; boundary=boundary\r\n\r\n--boundary\r\nContent-Type: text/plain; charset=utf-8\r\nContent-Transfer-Encoding: quoted-printable\r\n\r\nA caf=C3=A9 reply\r\n--boundary\r\nContent-Type: text/html; charset=utf-8\r\n\r\n<p>HTML alternative</p>\r\n--boundary--\r\n",
	}
	for name, raw := range raws {
		box, _ := user.GetMailbox(name)
		mb := box.(*memory.Mailbox)
		mb.Messages = []*memory.Message{{Uid: 42, Date: time.Now(), Flags: []string{imap.FlaggedFlag}, Size: uint32(len(raw)), Body: []byte(raw)}}
	}
	ln, err := tls.Listen("tcp", "127.0.0.1:0", &tls.Config{Certificates: []tls.Certificate{cert}})
	if err != nil {
		t.Fatal(err)
	}
	srv := server.New(be)
	srv.ErrorLog = log.New(io.Discard, "", 0)
	done := make(chan struct{})
	go func() { defer close(done); _ = srv.Serve(ln) }()
	t.Cleanup(func() { srv.Close(); ln.Close(); <-done })
	c := credentialConfig(t)
	c.IMAPHost = "127.0.0.1"
	c.IMAPPort = ln.Addr().(*net.TCPAddr).Port
	c.IMAPUser = "username"
	return imapTransport{roots: roots}, c
}
func TestIMAPDownloadsDecodedMailAndExpandsAcrossFolders(t *testing.T) {
	b, c := liveIMAPFixture(t)
	result := b.Sync(context.Background(), c, "password", accountMail{}, syncRequest{Folder: "INBOX", Limit: 50})
	if result.Err != nil {
		t.Fatal(result.Err)
	}
	if len(result.Data.Messages) != 1 {
		t.Fatalf("expected inbox message, got %d", len(result.Data.Messages))
	}
	m := result.Data.Messages[0]
	if m.Seen || !m.Starred || !strings.Contains(m.Body, "café reply") || strings.Contains(m.Body, "HTML alternative") || !strings.Contains(m.ReplyAddress, "<replies@example.com>") {
		t.Fatalf("incorrect MIME/flags/header: %+v", m)
	}
	result = b.Sync(context.Background(), c, "password", result.Data, syncRequest{Folder: "INBOX", ThreadID: m.MessageID})
	if result.Err != nil {
		t.Fatal(result.Err)
	}
	if len(result.Data.Messages) != 2 {
		t.Fatal("older parent in Archive was not found")
	}
	for _, r := range result.Data.Messages {
		if r.Seen {
			t.Fatal("background BODY.PEEK fetch marked real messages read")
		}
	}
	result.Data.Flags = []flagChange{{ID: "read-intent", Ref: m.Ref, Flag: imap.SeenFlag, Value: true}}
	result = b.Sync(context.Background(), c, "password", result.Data, syncRequest{Folder: "INBOX"})
	if result.Err != nil {
		t.Fatal(result.Err)
	}
	if len(result.Applied) != 1 {
		t.Fatal("flag change was not acknowledged")
	}
	for _, r := range result.Data.Messages {
		if r.Ref == m.Ref && (!r.Seen || !r.Starred) {
			t.Fatal("STORE must update Seen while retaining Flagged")
		}
	}
}
func TestIMAPStaleValidityDoesNotWriteAndSentCopyIsIdempotent(t *testing.T) {
	b, c := liveIMAPFixture(t)
	a := b.Sync(context.Background(), c, "password", accountMail{}, syncRequest{Folder: "INBOX"}).Data
	a.Flags = []flagChange{{ID: "stale", Ref: mailRef{"INBOX", 999, 42}, Flag: imap.SeenFlag, Value: true}}
	result := b.Sync(context.Background(), c, "password", a, syncRequest{Folder: "INBOX"})
	if result.Err != nil {
		t.Fatal(result.Err)
	}
	if len(result.Applied) != 1 || result.Data.Messages[0].Seen {
		t.Fatal("stale UIDVALIDITY must not target a reused UID")
	}
	o := outgoingMail{ID: "<sent-copy@example.com>", To: "friend@example.com", Subject: "Sent test", Body: "Already accepted by SMTP", Date: time.Now()}
	wire, _, err := makeWire(c, o)
	if err != nil {
		t.Fatal(err)
	}
	o.Wire = wire
	for i := 0; i < 2; i++ {
		if err := b.FileSent(context.Background(), c, "password", result.Data, o); err != nil {
			t.Fatal(err)
		}
	}
	result = b.Sync(context.Background(), c, "password", result.Data, syncRequest{Folder: "Sent"})
	if result.Err != nil {
		t.Fatal(result.Err)
	}
	count := 0
	for _, r := range result.Data.Messages {
		if r.MessageID == o.ID {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("Sent APPEND retries created %d copies", count)
	}
}

func smtpDeliveryFixture(t *testing.T, mode string) (imapTransport, AccountConfig, <-chan []byte) {
	t.Helper()
	cert, roots := testCertificate(t)
	startTLS := strings.HasPrefix(mode, "starttls-")
	mode = strings.TrimPrefix(mode, "starttls-")
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	bodies := make(chan []byte, 1)
	done := make(chan struct{})
	go func() {
		defer close(done)
		defer close(bodies)
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		if !startTLS {
			conn = tls.Server(conn, &tls.Config{Certificates: []tls.Certificate{cert}})
		}
		encrypted := !startTLS
		conn.SetDeadline(time.Now().Add(4 * time.Second))
		fmt.Fprint(conn, "220 localhost test\r\n")
		reader := textproto.NewReader(bufio.NewReader(conn))
		recipients := 0
		for {
			line, err := reader.ReadLine()
			if err != nil {
				return
			}
			fields := strings.Fields(line)
			if len(fields) == 0 {
				return
			}
			switch strings.ToUpper(fields[0]) {
			case "EHLO":
				if !encrypted {
					fmt.Fprint(conn, "250-localhost\r\n250-STARTTLS\r\n250 AUTH PLAIN\r\n")
				} else {
					fmt.Fprint(conn, "250-localhost\r\n250 AUTH PLAIN\r\n")
				}
			case "STARTTLS":
				fmt.Fprint(conn, "220 upgrade\r\n")
				conn = tls.Server(conn, &tls.Config{Certificates: []tls.Certificate{cert}})
				reader = textproto.NewReader(bufio.NewReader(conn))
				encrypted = true
			case "AUTH":
				if !encrypted {
					return
				}
				fmt.Fprint(conn, "235 authenticated\r\n")
			case "MAIL":
				fmt.Fprint(conn, "250 sender accepted\r\n")
			case "RCPT":
				recipients++
				if mode == "reject-recipient" && recipients == 2 {
					fmt.Fprint(conn, "550 rejected\r\n")
				} else {
					fmt.Fprint(conn, "250 recipient accepted\r\n")
				}
			case "DATA":
				fmt.Fprint(conn, "354 send message\r\n")
				body, err := reader.ReadDotBytes()
				if err != nil {
					return
				}
				bodies <- body
				if mode == "drop-after-data" {
					return
				}
				if mode == "reject-data" {
					fmt.Fprint(conn, "554 rejected\r\n")
				} else {
					fmt.Fprint(conn, "250 queued\r\n")
				}
			case "QUIT":
				fmt.Fprint(conn, "221 bye\r\n")
				return
			default:
				fmt.Fprint(conn, "250 okay\r\n")
			}
		}
	}()
	t.Cleanup(func() {
		ln.Close()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Error("SMTP fixture did not stop")
		}
	})
	c := credentialConfig(t)
	c.SMTPHost = "127.0.0.1"
	c.SMTPPort = ln.Addr().(*net.TCPAddr).Port
	c.SMTPSecurity = "TLS"
	if startTLS {
		c.SMTPSecurity = "STARTTLS"
	}
	return imapTransport{roots: roots}, c, bodies
}
func TestSMTPAcceptanceFailureAndAmbiguity(t *testing.T) {
	for _, mode := range []string{"accept", "starttls-accept", "reject-recipient", "reject-data", "drop-after-data"} {
		t.Run(mode, func(t *testing.T) {
			b, c, bodies := smtpDeliveryFixture(t, mode)
			o := outgoingMail{ID: "<send@example.com>", To: "one@example.com, two@example.com", Subject: "Test delivery", Body: "Hello\n.dot line", Date: time.Now()}
			wire, _, err := makeWire(c, o)
			if err != nil {
				t.Fatal(err)
			}
			o.Wire = wire
			result := b.Send(context.Background(), c, "password", o)
			if result.Accepted != (mode == "accept" || mode == "starttls-accept") || result.Uncertain != (mode == "drop-after-data") {
				t.Fatalf("incorrect acceptance classification: %+v", result)
			}
			body := <-bodies
			if mode == "reject-recipient" && len(body) != 0 {
				t.Fatal("partial recipient acceptance must not deliver any mail")
			}
			if mode == "accept" && !strings.Contains(string(body), ".dot line") {
				t.Fatal("SMTP dot stuffing corrupted content")
			}
		})
	}
}

func TestUncertainRetryRecognizesSentCopyWithoutSMTP(t *testing.T) {
	b, c := liveIMAPFixture(t)
	a := b.Sync(context.Background(), c, "password", accountMail{}, syncRequest{Folder: "INBOX"}).Data
	o := outgoingMail{ID: "<already-delivered@example.com>", To: "friend@example.com", Subject: "Already sent", Body: "Do not send twice", Date: time.Now()}
	var err error
	o.Wire, _, err = makeWire(c, o)
	if err != nil {
		t.Fatal(err)
	}
	if err = b.FileSent(context.Background(), c, "password", a, o); err != nil {
		t.Fatal(err)
	}
	c.SMTPHost = "127.0.0.1"
	c.SMTPPort = 1
	o.State = "retry"
	result := b.Send(context.Background(), c, "password", o)
	if !result.Accepted || result.Err != nil {
		t.Fatal("existing Sent copy should confirm delivery without opening SMTP", result.Err)
	}
}
