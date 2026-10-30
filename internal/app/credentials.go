package app

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
)

// Keep existing credential identities across the rename; passwords need no migration.
const credentialService = "Sorakyaku mail"
const credentialCollectionName = "Sorakyaku"

// Bind a password to the authenticated destinations, not just a configurable
// identifier. Editing a host or login in accounts.json must not forward the old
// password to a new server. Display names and folders may change independently.
func credentialKey(c AccountConfig) string {
	b, _ := json.Marshal([]any{strings.ToLower(c.Email), strings.ToLower(c.IMAPHost), c.IMAPPort, c.IMAPUser, strings.ToLower(c.SMTPHost), c.SMTPPort, c.SMTPUser, c.SMTPSecurity})
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

type credentialStore interface {
	Get(context.Context, AccountConfig) (string, error)
	Set(context.Context, AccountConfig, string) error
}

type systemCredentials struct{}

// A child process bounds native keyring calls, including a stalled D-Bus
// service. Secrets travel only through anonymous pipes, never command arguments.
func keyringCommand(ctx context.Context, operation string, c AccountConfig, password string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Minute)
	defer cancel()
	self, err := os.Executable()
	if err != nil {
		return "", errors.New("Cannot locate the keyring helper.")
	}
	cmd := exec.CommandContext(ctx, self, "__keyring", operation, credentialKey(c))
	cmd.Stdin = strings.NewReader(password)
	data, err := cmd.Output()
	if err != nil {
		return "", errors.New("Local password unavailable. Unlock your system keyring, or run hane --password-setup.")
	}
	return string(data), nil
}
func (systemCredentials) Get(ctx context.Context, c AccountConfig) (string, error) {
	return keyringCommand(ctx, "get", c, "")
}
func (systemCredentials) Set(ctx context.Context, c AccountConfig, password string) error {
	_, err := keyringCommand(ctx, "set", c, password)
	return err
}

func runKeyringHelper(args []string) int {
	if len(args) != 2 || len(args[1]) != 64 {
		return 1
	}
	if _, err := hex.DecodeString(args[1]); err != nil {
		return 1
	}
	switch args[0] {
	case "get":
		password, err := nativeCredentialGet(args[1])
		if err != nil {
			return 1
		}
		fmt.Fprint(os.Stdout, password)
	case "set":
		data, err := io.ReadAll(io.LimitReader(os.Stdin, 4097))
		if err != nil || len(data) == 0 || len(data) > 4096 {
			return 1
		}
		if err = nativeCredentialSet(args[1], string(data)); err != nil {
			return 1
		}
	default:
		return 1
	}
	return 0
}

type reconnectMsg struct {
	index int
	key   string
	err   error
}

func (m model) reconnectAccounts() tea.Cmd {
	if m.credentials == nil {
		return nil
	}
	var cmds []tea.Cmd
	for i, a := range m.accounts {
		if a.Config == nil || !a.Config.RememberPassword {
			continue
		}
		c, index, store := *a.Config, i, m.credentials
		parent := m.background
		if parent == nil {
			parent = context.Background()
		}
		cmds = append(cmds, func() tea.Msg {
			ctx, cancel := context.WithTimeout(parent, 4*time.Minute)
			defer cancel()
			password, err := store.Get(ctx, c)
			if err == nil && password == "" {
				err = errors.New("Saved password is empty. Run hane --password-setup.")
			}
			if err == nil {
				_, err = checkAccount(ctx, c, password)
			}
			return reconnectMsg{index, credentialKey(c), err}
		})
	}
	return tea.Sequence(cmds...)
}

func (m model) finishReconnect(msg reconnectMsg) (tea.Model, tea.Cmd) {
	if msg.index < 0 || msg.index >= len(m.accounts) {
		return m, nil
	}
	a := &m.accounts[msg.index]
	if a.Config == nil || !a.Config.RememberPassword || credentialKey(*a.Config) != msg.key {
		return m, nil
	}
	// An explicit connection check is newer than the startup attempt.
	if a.Verified {
		return m, nil
	}
	a.Verified = msg.err == nil
	a.ConnectionStatus = "Sign-in verified · saved password"
	if msg.err != nil {
		a.ConnectionStatus = msg.err.Error()
	}
	return m, nil
}
