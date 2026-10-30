package app

import (
	"bufio"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"fmt"
	"math/big"
	"net"
	"strings"
	"testing"
	"time"
)

func testCertificate(t *testing.T) (tls.Certificate, *x509.CertPool) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{SerialNumber: big.NewInt(1), NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), IPAddresses: []net.IP{net.ParseIP("127.0.0.1")}, KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}, BasicConstraintsValid: true}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	roots := x509.NewCertPool()
	roots.AddCert(cert)
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}, roots
}
func fakeMailServer(t *testing.T, cert tls.Certificate, protocol, security, failure string) (int, chan string) {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	commands := make(chan string, 40)
	done := make(chan struct{})
	go func() {
		defer close(done)
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		conn.SetDeadline(time.Now().Add(3 * time.Second))
		encrypted := security == "TLS"
		tlsConfig := &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS12}
		if encrypted {
			conn = tls.Server(conn, tlsConfig)
		}
		if failure == "stall" {
			buf := make([]byte, 1)
			conn.Read(buf)
			return
		}
		if protocol == "imap" {
			fmt.Fprint(conn, "* OK [CAPABILITY IMAP4rev1] ready\r\n")
		} else {
			fmt.Fprint(conn, "220 localhost test\r\n")
		}
		reader := bufio.NewReader(conn)
		for {
			line, err := reader.ReadString('\n')
			if err != nil {
				return
			}
			line = strings.TrimSpace(line)
			commands <- line
			fields := strings.Fields(line)
			if len(fields) == 0 {
				continue
			}
			if protocol == "imap" {
				if len(fields) < 2 {
					return
				}
				tag, verb := fields[0], strings.ToUpper(fields[1])
				switch verb {
				case "CAPABILITY":
					fmt.Fprintf(conn, "* CAPABILITY IMAP4rev1\r\n%s OK capability\r\n", tag)
				case "LOGIN":
					if failure == "auth" {
						fmt.Fprintf(conn, "%s NO secret-test-password rejected\r\n", tag)
					} else {
						fmt.Fprintf(conn, "%s OK logged in\r\n", tag)
					}
				case "LIST":
					fmt.Fprintf(conn, "* LIST () \"/\" \"INBOX\"\r\n* LIST (\\Noselect) \"/\" \"Container\"\r\n* LIST () \"/\" \"Archive\"\r\n%s OK listed\r\n", tag)
				default:
					fmt.Fprintf(conn, "%s BAD unexpected command\r\n", tag)
				}
			} else {
				switch strings.ToUpper(fields[0]) {
				case "EHLO":
					if !encrypted && failure != "no-starttls" {
						fmt.Fprint(conn, "250-localhost\r\n250-STARTTLS\r\n250 AUTH PLAIN\r\n")
					} else {
						fmt.Fprint(conn, "250-localhost\r\n250 AUTH PLAIN\r\n")
					}
				case "STARTTLS":
					fmt.Fprint(conn, "220 start TLS\r\n")
					conn = tls.Server(conn, tlsConfig)
					reader = bufio.NewReader(conn)
					encrypted = true
				case "AUTH":
					if !encrypted {
						commands <- "PLAINTEXT AUTH"
						return
					}
					payload, _ := base64.StdEncoding.DecodeString(fields[len(fields)-1])
					if failure == "auth" || !strings.HasSuffix(string(payload), "\x00secret-test-password") {
						fmt.Fprint(conn, "535 secret-test-password rejected\r\n")
					} else {
						fmt.Fprint(conn, "235 authenticated\r\n")
					}
				case "QUIT":
					fmt.Fprint(conn, "221 bye\r\n")
					return
				default:
					fmt.Fprint(conn, "500 unexpected command\r\n")
				}
			}
		}
	}()
	t.Cleanup(func() {
		listener.Close()
		select {
		case <-done:
		case <-time.After(4 * time.Second):
			t.Error("test server did not stop")
		}
	})
	return port, commands
}
func connectionConfig(imapPort, smtpPort int, security string) AccountConfig {
	return AccountConfig{Name: "Test", Email: "test@example.com", Provider: "Custom", IMAPHost: "127.0.0.1", IMAPPort: imapPort, IMAPUser: "test@example.com", SMTPHost: "127.0.0.1", SMTPPort: smtpPort, SMTPUser: "test@example.com", SMTPSecurity: security}
}
func TestAccountChecksTLSAndSTARTTLSWithoutSending(t *testing.T) {
	for _, security := range []string{"TLS", "STARTTLS"} {
		t.Run(security, func(t *testing.T) {
			cert, roots := testCertificate(t)
			imapPort, imapCommands := fakeMailServer(t, cert, "imap", "TLS", "")
			smtpPort, smtpCommands := fakeMailServer(t, cert, "smtp", security, "")
			folders, err := checkAccountWithRoots(context.Background(), connectionConfig(imapPort, smtpPort, security), "secret-test-password", roots)
			if err != nil {
				t.Fatal(err)
			}
			if strings.Join(folders, ",") != "INBOX,Archive" {
				t.Fatalf("unexpected folders: %v", folders)
			}
			for _, commands := range []chan string{imapCommands, smtpCommands} {
				for len(commands) > 0 {
					line := <-commands
					upper := strings.ToUpper(line)
					for _, bad := range []string{" SELECT ", " STORE ", "MAIL FROM", "RCPT TO", "DATA", "PLAINTEXT AUTH"} {
						if strings.Contains(upper, bad) {
							t.Fatalf("connection check changed mail or exposed credentials: %s", bad)
						}
					}
				}
			}
		})
	}
}
func TestConnectionErrorsAreActionableAndRedacted(t *testing.T) {
	for _, stage := range []string{"imap-auth", "smtp-auth", "no-starttls", "untrusted-cert"} {
		t.Run(stage, func(t *testing.T) {
			cert, roots := testCertificate(t)
			imapFailure, smtpFailure := "", ""
			if stage == "imap-auth" {
				imapFailure = "auth"
			}
			if stage == "smtp-auth" {
				smtpFailure = "auth"
			}
			if stage == "no-starttls" {
				smtpFailure = "no-starttls"
			}
			imapPort, _ := fakeMailServer(t, cert, "imap", "TLS", imapFailure)
			smtpPort, _ := fakeMailServer(t, cert, "smtp", "STARTTLS", smtpFailure)
			if stage == "untrusted-cert" {
				roots = x509.NewCertPool()
			}
			_, err := checkAccountWithRoots(context.Background(), connectionConfig(imapPort, smtpPort, "STARTTLS"), "secret-test-password", roots)
			if err == nil || strings.Contains(err.Error(), "secret-test-password") {
				t.Fatalf("expected redacted error: %v", err)
			}
			expected := "IMAP sign-in"
			switch stage {
			case "smtp-auth":
				expected = "SMTP sign-in"
			case "no-starttls":
				expected = "STARTTLS"
			case "untrusted-cert":
				expected = "certificate"
			}
			if !strings.Contains(err.Error(), expected) {
				t.Fatalf("missing useful stage %s: %v", expected, err)
			}
		})
	}
}
func TestConnectionCanBeCancelledDuringGreeting(t *testing.T) {
	cert, roots := testCertificate(t)
	port, _ := fakeMailServer(t, cert, "imap", "TLS", "stall")
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err := checkAccountWithRoots(ctx, connectionConfig(port, 1, "TLS"), "secret-test-password", roots)
	if err == nil || time.Since(start) > time.Second {
		t.Fatalf("timeout did not interrupt connection: %v", err)
	}
	ctx, cancel = context.WithCancel(context.Background())
	cancel()
	_, err = checkAccountWithRoots(ctx, connectionConfig(port, 1, "TLS"), "secret-test-password", roots)
	if err == nil || !strings.Contains(err.Error(), "cancelled") {
		t.Fatalf("expected cancellation: %v", err)
	}
}
