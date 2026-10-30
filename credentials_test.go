package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func credentialConfig(t *testing.T) AccountConfig { t.Helper(); return setupModel(t).setup.config() }

func TestCredentialBoundToDestinations(t *testing.T) {
	c := credentialConfig(t)
	key := credentialKey(c)
	changes := []func(*AccountConfig){
		func(c *AccountConfig) { c.Email = "other@example.com" },
		func(c *AccountConfig) { c.IMAPHost = "attacker.example.com" },
		func(c *AccountConfig) { c.SMTPHost = "attacker.example.com" },
		func(c *AccountConfig) { c.IMAPPort++ },
		func(c *AccountConfig) { c.SMTPPort++ },
		func(c *AccountConfig) { c.IMAPUser += "other" },
		func(c *AccountConfig) { c.SMTPUser += "other" },
		func(c *AccountConfig) { c.SMTPSecurity = "TLS" },
	}
	for i, change := range changes {
		other := c
		change(&other)
		if credentialKey(other) == key {
			t.Fatalf("change %d reused password binding", i)
		}
	}
	c.Name, c.Folders = "New name", []string{"INBOX", "Archive"}
	if credentialKey(c) != key {
		t.Fatal("display changes must preserve the binding")
	}
}

type failingCredentials struct{ called bool }

func (s *failingCredentials) Get(ctx context.Context, c AccountConfig) (string, error) {
	s.called = true
	if ctx.Err() != nil {
		return "", ctx.Err()
	}
	return "", errors.New("Keyring unavailable")
}
func (*failingCredentials) Set(context.Context, AccountConfig, string) error {
	panic("unexpected write")
}

func TestReconnectOnlyOptedInAndKeepsUIUsable(t *testing.T) {
	m := newModel(time.Now())
	store := &failingCredentials{}
	m.credentials = store
	c := credentialConfig(t)
	m.appendConfiguredAccount(c, false)
	if m.Init() != nil {
		t.Fatal("legacy accounts must not read keyring or connect")
	}
	m.accounts[2].Config.RememberPassword = true
	m.openAccountSetup(-1)
	msg := m.Init()().(reconnectMsg)
	if !store.called || msg.err == nil {
		t.Fatal("keyring failure must become an app message")
	}
	next, _ := m.Update(msg)
	m = next.(model)
	if m.setup == nil || m.accounts[2].Verified || !strings.Contains(m.accounts[2].ConnectionStatus, "unavailable") {
		t.Fatal("background failure must not disrupt account editing")
	}
	m.accounts[2].Config.IMAPHost = "new.example.com"
	next, _ = m.Update(reconnectMsg{index: 2, key: msg.key})
	if next.(model).accounts[2].Verified {
		t.Fatal("stale check verified a different destination")
	}
}

func TestReconnectCancelledWithApp(t *testing.T) {
	m := newModel(time.Now())
	ctx, cancel := context.WithCancel(context.Background())
	m.background, m.credentials = ctx, &failingCredentials{}
	c := credentialConfig(t)
	c.RememberPassword = true
	m.appendConfiguredAccount(c, false)
	cmd := m.Init()
	cancel()
	if !errors.Is(cmd().(reconnectMsg).err, context.Canceled) {
		t.Fatal("app shutdown must cancel background work")
	}
}

func TestRememberedAccountEditRules(t *testing.T) {
	for _, mode := range []string{"cached", "typed", "server-changed"} {
		t.Run(mode, func(t *testing.T) {
			m := setupModel(t)
			c := m.setup.config()
			c.RememberPassword = true
			c.BitwardenItem = "11111111-2222-3333-4444-555555555555"
			m.appendConfiguredAccount(c, false)
			m.openAccountSetup(2)
			if mode == "typed" {
				m.setup.values[3] = "replacement-test-secret"
			}
			if mode == "server-changed" {
				m.setup.values[4] = "changed.example.com"
				m.setup.values[3] = "replacement-test-secret"
			}
			m.setup.busy = true
			next, _ := m.finishAccountCheck(accountCheckMsg{id: m.setup.id, folders: []string{"INBOX"}})
			m = next.(model)
			saved, err := loadAccountConfigs(m.configPath)
			if err != nil {
				t.Fatal(err)
			}
			if saved[0].RememberPassword != (mode == "cached") {
				t.Fatal("only an unchanged, cached password should retain automatic sign-in")
			}
			if mode == "server-changed" && saved[0].BitwardenItem != "" {
				t.Fatal("server edit must remove cloud binding")
			}
			data, _ := os.ReadFile(m.configPath)
			if strings.Contains(string(data), "replacement-test-secret") {
				t.Fatal("config leaked password")
			}
		})
	}
}

func fakeBitwarden(t *testing.T, body string) *bitwardenClient {
	t.Helper()
	dir := t.TempDir()
	p := filepath.Join(dir, "bw")
	if err := os.WriteFile(p, []byte("#!/bin/sh\nset -eu\n"+body), 0700); err != nil {
		t.Fatal(err)
	}
	return &bitwardenClient{executable: p, directory: dir, session: "test-session-value"}
}

func TestBitwardenUsesPipesAndPrivateSession(t *testing.T) {
	t.Setenv("BW_SESSION", "unrelated-user-session")
	t.Setenv("BW_RAW", "true")
	b := fakeBitwarden(t, `
test "$BW_SESSION" = test-session-value
test -z "${BW_RAW:-}"
test "$BW_NOINTERACTION" = true
test "$#" = 2
test "$1" = create
test "$2" = item
payload=$(cat)
test -n "$payload"
printf '%s' '{"id":"11111111-2222-3333-4444-555555555555"}'
`)
	id, err := b.create(context.Background(), credentialConfig(t), "secret-with-`backticks`-$(shell)")
	if err != nil || !bitwardenID.MatchString(id) {
		t.Fatalf("private create failed: %v", err)
	}
}

func TestBitwardenReadsOnlyMatchingLogin(t *testing.T) {
	c := credentialConfig(t)
	const id = "11111111-2222-3333-4444-555555555555"
	for _, wrong := range []bool{false, true} {
		item := vaultItem{ID: id, Type: 1}
		item.Login.Username = c.Email
		item.Login.Password = "fixture-password"
		if wrong {
			item.Login.Username = "different@example.com"
		}
		data, _ := json.Marshal(item)
		b := fakeBitwarden(t, "test \"$1\" = get\ntest \"$2\" = item\ntest \"$3\" = "+id+"\nprintf '%s' '"+string(data)+"'\n")
		password, err := b.get(context.Background(), id, c)
		if wrong {
			if err == nil || password != "" {
				t.Fatal("unrelated login must be rejected")
			}
		} else if err != nil || password != "fixture-password" {
			t.Fatal("matching item not retrieved")
		}
		if _, err := b.get(context.Background(), "--help", c); err == nil {
			t.Fatal("non-UUID must be rejected before invoking CLI")
		}
	}
}

func TestBitwardenErrorsNeverEchoSecret(t *testing.T) {
	b := fakeBitwarden(t, "echo 'fixture-secret-and-session' >&2\nexit 1\n")
	_, err := b.command(context.Background(), nil, false, "get", "item", "test")
	if err == nil || strings.Contains(err.Error(), "fixture-secret") {
		t.Fatal("raw CLI errors may contain credentials")
	}
	_, err = b.create(context.Background(), credentialConfig(t), "fixture-secret")
	if err == nil || !strings.Contains(err.Error(), "before retrying") {
		t.Fatal("ambiguous cloud write must explain duplicate recovery")
	}
}
