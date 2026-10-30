package app

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/smtp"
	"strings"
	"time"

	"github.com/emersion/go-imap"
	"github.com/emersion/go-imap/client"
)

type connectionProblem struct{ Stage, Message string }

func (e *connectionProblem) Error() string { return e.Stage + ": " + e.Message }
func connectionError(ctx context.Context, stage string, err error, auth bool) error {
	message := "Connection failed. Check the server and port, then retry."
	var dns *net.DNSError
	var cert *tls.CertificateVerificationError
	var unknown x509.UnknownAuthorityError
	var host x509.HostnameError
	var op net.Error
	switch {
	case errors.Is(ctx.Err(), context.Canceled):
		message = "Connection cancelled."
	case errors.Is(ctx.Err(), context.DeadlineExceeded) || errors.As(err, &op) && op.Timeout():
		message = "Connection timed out. Check the network and server port."
	case errors.As(err, &dns):
		message = "Server not found. Check the hostname and network."
	case errors.As(err, &cert) || errors.As(err, &unknown) || errors.As(err, &host):
		message = "TLS certificate could not be verified. Check the hostname and system clock."
	case auth:
		message = "Sign-in rejected. Check the login and app password; your provider may require OAuth."
	}
	// Do not expose raw protocol errors: some servers echo submitted credentials.
	return &connectionProblem{stage, message}
}

// A connection check authenticates and lists folders, then disconnects. It never
// selects a mailbox, marks messages read, or sends an email.
func checkAccount(ctx context.Context, c AccountConfig, password string) ([]string, error) {
	return checkAccountWithRoots(ctx, c, password, nil)
}

func checkAccountWithRoots(ctx context.Context, c AccountConfig, password string, roots *x509.CertPool) ([]string, error) {
	ctx, cancel := context.WithTimeout(ctx, 25*time.Second)
	defer cancel()
	if _, msg := c.validate(); msg != "" {
		return nil, errors.New(msg)
	}
	if password == "" {
		return nil, errors.New("Enter an app password.")
	}
	conn, err := (&net.Dialer{}).DialContext(ctx, "tcp", net.JoinHostPort(c.IMAPHost, fmt.Sprint(c.IMAPPort)))
	if err != nil {
		return nil, connectionError(ctx, "IMAP", err, false)
	}
	defer conn.Close()
	stop := context.AfterFunc(ctx, func() { conn.Close() })
	defer stop()
	deadline, _ := ctx.Deadline()
	conn.SetDeadline(deadline)
	secured := tls.Client(conn, &tls.Config{ServerName: c.IMAPHost, MinVersion: tls.VersionTLS12, RootCAs: roots})
	if err = secured.HandshakeContext(ctx); err != nil {
		return nil, connectionError(ctx, "IMAP TLS", err, false)
	}
	im, err := client.New(secured)
	if err != nil {
		return nil, connectionError(ctx, "IMAP greeting", err, false)
	}
	im.ErrorLog = log.New(io.Discard, "", 0)
	defer im.Terminate()
	if err = im.Login(c.IMAPUser, password); err != nil {
		return nil, connectionError(ctx, "IMAP sign-in", err, true)
	}
	boxes := make(chan *imap.MailboxInfo, 32)
	done := make(chan error, 1)
	go func() { done <- im.List("", "*", boxes) }()
	var folders []string
	seen := map[string]bool{}
	for box := range boxes {
		selectable := true
		for _, flag := range box.Attributes {
			if strings.EqualFold(flag, imap.NoSelectAttr) {
				selectable = false
			}
		}
		if selectable && safeField(box.Name) && !seen[box.Name] && len(folders) < 500 {
			folders = append(folders, box.Name)
			seen[box.Name] = true
		}
	}
	if err = <-done; err != nil {
		return nil, connectionError(ctx, "IMAP folders", err, false)
	}
	if err = checkSMTP(ctx, c, password, roots); err != nil {
		return nil, err
	}
	return folders, nil
}
func checkSMTP(ctx context.Context, c AccountConfig, password string, roots *x509.CertPool) error {
	conn, err := (&net.Dialer{}).DialContext(ctx, "tcp", net.JoinHostPort(c.SMTPHost, fmt.Sprint(c.SMTPPort)))
	if err != nil {
		return connectionError(ctx, "SMTP", err, false)
	}
	defer conn.Close()
	stop := context.AfterFunc(ctx, func() { conn.Close() })
	defer stop()
	deadline, _ := ctx.Deadline()
	conn.SetDeadline(deadline)
	tlsConfig := &tls.Config{ServerName: c.SMTPHost, MinVersion: tls.VersionTLS12, RootCAs: roots}
	var transport net.Conn = conn
	if c.SMTPSecurity == "TLS" {
		secured := tls.Client(conn, tlsConfig)
		if err = secured.HandshakeContext(ctx); err != nil {
			return connectionError(ctx, "SMTP TLS", err, false)
		}
		transport = secured
	}
	s, err := smtp.NewClient(transport, c.SMTPHost)
	if err != nil {
		return connectionError(ctx, "SMTP greeting", err, false)
	}
	defer s.Close()
	if c.SMTPSecurity == "STARTTLS" {
		if ok, _ := s.Extension("STARTTLS"); !ok {
			return &connectionProblem{"SMTP TLS", "Server does not offer STARTTLS. Check the port or select TLS."}
		}
		if err = s.StartTLS(tlsConfig); err != nil {
			return connectionError(ctx, "SMTP TLS", err, false)
		}
	}
	if ok, methods := s.Extension("AUTH"); !ok || !strings.Contains(strings.ToUpper(methods), "PLAIN") {
		return &connectionProblem{"SMTP sign-in", "Server does not offer password authentication (PLAIN). OAuth-only accounts are not supported yet."}
	}
	if err = s.Auth(smtp.PlainAuth("", c.SMTPUser, password, c.SMTPHost)); err != nil {
		return connectionError(ctx, "SMTP sign-in", err, true)
	}
	return nil
}
