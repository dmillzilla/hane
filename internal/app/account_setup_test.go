package app

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
)

func setupModel(t *testing.T) model {
	t.Helper()
	m := newModel(time.Now())
	m.configPath = filepath.Join(t.TempDir(), "hane", "accounts.json")
	m.openAccountSetup(-1)
	m.setup.values[1] = "Personal iCloud"
	m.setup.values[2] = "me@example.com"
	m.setup.values[3] = "secret-test-password"
	return m
}
func TestAddAccountAtExplorerBottom(t *testing.T) {
	m := newModel(time.Now())
	rows := m.rows()
	if rows[len(rows)-1].account != -1 {
		t.Fatal("Add Account must be the last explorer row")
	}
	m = press(m, "\\nfG")
	if m.rows()[m.treeCursor].account != -1 {
		t.Fatal("G should navigate to Add Account")
	}
	m = press(m, "h") // Must not index account -1.
	m = special(m, tea.KeyEnter)
	if m.setup == nil {
		t.Fatal("Enter must open account form")
	}
	m = special(m, tea.KeyEscape)
	y := 7 + m.treeCursor - m.treeOffset
	next, _ := m.Update(tea.MouseClickMsg{X: 5, Y: y, Button: tea.MouseLeft})
	m = next.(model)
	if m.setup == nil {
		t.Fatal("click must open account form")
	}
	m = special(m, tea.KeyEscape)
	m = press(m, "A")
	if m.setup == nil {
		t.Fatal("A must open form")
	}
}
func TestSetupFieldsAndValidation(t *testing.T) {
	m := setupModel(t)
	m.setup.values[2] = ""
	m.setup.cursor = 0
	m = press(m, "iqhjkl")
	if m.quitOpen || m.setup.values[2] != "qhjkl" {
		t.Fatal("letters must type inside form instead of navigating underlying app")
	}
	m = control(m, 'u')
	m = press(m, "name@example.com")
	m = special(m, tea.KeyTab)
	if m.setup.focus != 3 {
		t.Fatal("Tab must advance fields")
	}
	m = press(m, "i")
	m = control(m, 'u')
	m = press(m, "sëcret")
	m = special(m, tea.KeyLeft)
	m = special(m, tea.KeyBackspace)
	if m.setup.values[3] != "sëcrt" {
		t.Fatal("field editing must preserve rune boundaries")
	}
	if strings.Contains(m.View().Content, "sëcrt") {
		t.Fatal("password must be masked")
	}
	next, _ := m.Update(tea.PasteMsg{Content: "\x1b\nPASTE"})
	m = next.(model)
	if strings.ContainsAny(m.setup.values[3], "\x1b\n") {
		t.Fatal("paste controls must be stripped")
	}
	m.setup.values[2] = "invalid"
	if cmd := m.connectAccount(); cmd != nil || m.setup.busy || m.setup.focus != 2 {
		t.Fatal("invalid email must focus field without connecting")
	}
	m.setup.values[2] = "name@example.com"
	m.setup.values[5] = "99999"
	if cmd := m.connectAccount(); cmd != nil || m.setup.focus != 5 {
		t.Fatal("invalid port must not connect")
	}
	m.setup.values[5] = "993"
	m.setup.values[4] = "https://imap.example.com"
	if cmd := m.connectAccount(); cmd != nil || m.setup.focus != 4 {
		t.Fatal("URLs must not pass hostname validation")
	}
	m.setup.focus = 0
	m.setup.values[0] = "iCloud"
	m = special(m, tea.KeyRight)
	if m.setup.values[0] != "Gmail" || m.setup.config().IMAPHost != "imap.gmail.com" {
		t.Fatal("provider selector must populate presets")
	}
	m.setup.focus = 10
	m = special(m, tea.KeyEnter)
	if m.setup.values[10] != "TLS" || m.setup.values[8] != "465" {
		t.Fatal("security selector must pair TLS and its usual port")
	}
}
func TestSetupCancelIgnoresLateConnectionResult(t *testing.T) {
	m := setupModel(t)
	id := m.setup.id
	ctx, cancel := context.WithCancel(context.Background())
	m.setup.cancel = cancel
	m.setup.busy = true
	next, _ := m.Update(tea.PasteMsg{Content: "ignored"})
	m = next.(model)
	if m.setup.values[3] != "secret-test-password" {
		t.Fatal("connecting must freeze edits")
	}
	m = special(m, tea.KeyEscape)
	if m.setup != nil || ctx.Err() != context.Canceled {
		t.Fatal("Esc must cancel network and dismiss form")
	}
	m.openAccountSetup(-1)
	next, _ = m.Update(accountCheckMsg{id: id, folders: []string{"INBOX"}})
	m = next.(model)
	if len(m.accounts) != 2 || m.setup.success {
		t.Fatal("late result must not add an account or modify a new form")
	}
	if _, err := os.Stat(m.configPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("cancelled check must not save settings")
	}
}
func TestAccountSuccessPersistsSettingsAndReopens(t *testing.T) {
	m := setupModel(t)
	m.setup.busy = true
	next, _ := m.Update(accountCheckMsg{id: m.setup.id, folders: []string{"INBOX", "Archive"}})
	m = next.(model)
	if len(m.accounts) != 3 || len(m.expanded) != 3 || !m.setup.success || m.setup.values[3] != "" || !m.accounts[2].Verified {
		t.Fatal("success must add a dynamic account and clear credentials")
	}
	file, err := os.ReadFile(m.configPath)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(file), "secret-test-password") || strings.Contains(string(file), "password") {
		t.Fatal("credentials must not be stored")
	}
	info, _ := os.Stat(m.configPath)
	if info.Mode().Perm() != 0600 {
		t.Fatal("account file must be private")
	}
	info, _ = os.Stat(filepath.Dir(m.configPath))
	if info.Mode().Perm() != 0700 {
		t.Fatal("new config directory must be private")
	}
	configs, err := loadAccountConfigs(m.configPath)
	if err != nil || len(configs) != 1 || configs[0].IMAPUser != "me" || configs[0].SMTPUser != "me@example.com" {
		t.Fatalf("saved preset did not round-trip: %v", err)
	}
	m = special(m, tea.KeyEnter)
	if m.setup != nil {
		t.Fatal("Done must dismiss success form")
	}
	m = press(m, "h") // From folder to its account header.
	m = press(m, "e")
	if m.setup == nil || m.setup.edit != 2 || m.setup.focus != 3 || m.setup.values[3] != "" {
		t.Fatal("e on configured account must reopen settings and request password")
	}
	m.setup.values[1] = "Renamed account"
	m.setup.busy = true
	next, _ = m.Update(accountCheckMsg{id: m.setup.id, folders: []string{"INBOX"}})
	m = next.(model)
	configs, err = loadAccountConfigs(m.configPath)
	if err != nil || len(configs) != 1 || len(m.accounts) != 3 || configs[0].Name != "Renamed account" {
		t.Fatal("editing must replace account, not duplicate it")
	}
	restored := newModel(time.Now())
	restored.appendConfiguredAccount(configs[0], false)
	if restored.accounts[2].Verified {
		t.Fatal("saved verification must not imply connection after restart")
	}
}
func TestSetupFailuresKeepFormAndNeverAddAccount(t *testing.T) {
	m := setupModel(t)
	m.setup.busy = true
	next, _ := m.Update(accountCheckMsg{id: m.setup.id, err: &connectionProblem{"SMTP sign-in", "Sign-in rejected."}})
	m = next.(model)
	if m.setup.busy || m.setup.success || len(m.accounts) != 2 || m.setup.values[3] != "secret-test-password" || !strings.Contains(m.setup.message, "SMTP") {
		t.Fatal("failed sign-in must retain fields and permit retry")
	}
	// A successful connection cannot silently ignore failed persistence.
	m.configPath = t.TempDir()
	m.setup.busy = true
	next, _ = m.Update(accountCheckMsg{id: m.setup.id, folders: []string{"INBOX"}})
	m = next.(model)
	if m.setup.success || len(m.accounts) != 2 || !strings.Contains(m.setup.message, "could not be saved") {
		t.Fatal("save failure must be actionable and keep setup open")
	}
	m.configError = "Account settings could not be loaded."
	if cmd := m.connectAccount(); cmd != nil || m.setup.busy {
		t.Fatal("invalid loaded config must block destructive overwrite")
	}
}
func TestAccountPopupFitsAndKeepsActiveFieldVisible(t *testing.T) {
	for _, size := range [][2]int{{150, 42}, {120, 36}, {80, 24}, {48, 16}, {30, 10}} {
		for focus := 0; focus < setupFields+2; focus++ {
			m := setupModel(t)
			m.width = size[0]
			m.height = size[1]
			m.setup.focus = focus
			lines := strings.Split(m.View().Content, "\n")
			if len(lines) != m.height {
				t.Fatalf("wrong height for %v", size)
			}
			for _, line := range lines {
				if ansi.StringWidth(line) != m.width {
					t.Fatalf("wrong width for %v: %d", size, ansi.StringWidth(line))
				}
			}
			if focus < setupFields && size[0] >= 48 && size[1] >= 16 && !strings.Contains(ansi.Strip(m.View().Content), setupLabels[focus]) {
				t.Fatalf("field %s not scrolled into view at %v", setupLabels[focus], size)
			}
			if cursor := m.View().Cursor; cursor != nil {
				x, y, w, h := m.setupGeometry()
				if cursor.X < x || cursor.X >= x+w || cursor.Y < y || cursor.Y >= y+h {
					t.Fatal("cursor must stay inside account modal")
				}
			}
		}
	}
}

func TestSetupMouseCancelAndSuccessButtons(t *testing.T) {
	for _, success := range []bool{false, true} {
		m := setupModel(t)
		m.setup.success = success
		x, y, w, h := m.setupGeometry()
		next, _ := m.Update(tea.MouseClickMsg{X: x + w - 5, Y: y + h - 3, Button: tea.MouseLeft})
		if next.(model).setup != nil {
			t.Fatal("mouse cancel/close must dismiss safely")
		}
	}
	m := setupModel(t)
	m.setup.success = true
	x, y, _, h := m.setupGeometry()
	next, _ := m.Update(tea.MouseClickMsg{X: x + 5, Y: y + h - 3, Button: tea.MouseLeft})
	if next.(model).setup != nil {
		t.Fatal("mouse Done must dismiss success")
	}
}
func TestConfigStartupAndDuplicateGuard(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	path, err := accountConfigPath()
	if err != nil {
		t.Fatal(err)
	}
	m := setupModel(t)
	c := m.setup.config()
	c.Folders = []string{"INBOX"}
	if err = saveAccountConfigs(path, []AccountConfig{c}); err != nil {
		t.Fatal(err)
	}
	restored := newModel(time.Now())
	restored.loadAccounts()
	if len(restored.accounts) != 3 || restored.accounts[2].Verified || restored.configError != "" {
		t.Fatal("startup must restore configuration without implying verified sign-in")
	}
	restored.openAccountSetup(-1)
	restored.setup.values[2] = "ME@EXAMPLE.COM"
	restored.setup.values[3] = "test"
	if cmd := restored.connectAccount(); cmd != nil || !strings.Contains(restored.setup.message, "already configured") {
		t.Fatal("duplicate detection must be case-insensitive and ignore demo accounts")
	}
	malformed := []byte("{broken settings")
	if err = os.WriteFile(path, malformed, 0600); err != nil {
		t.Fatal(err)
	}
	restored = newModel(time.Now())
	restored.loadAccounts()
	restored.openAccountSetup(-1)
	if restored.configError == "" || len(restored.accounts) != 2 {
		t.Fatal("invalid file must leave demo usable and expose error")
	}
	if cmd := restored.connectAccount(); cmd != nil {
		t.Fatal("must not overwrite unreadable settings")
	}
	after, _ := os.ReadFile(path)
	if string(after) != string(malformed) {
		t.Fatal("invalid settings file was overwritten")
	}
}

func TestAccountSetupVimModes(t *testing.T) {
	m := setupModel(t)
	original := m.setup.values
	if m.setup.insert || m.View().Cursor.Shape != tea.CursorBlock {
		t.Fatal("setup must open in Normal with a block cursor")
	}
	m = press(m, "jklh")
	if m.setup.focus != 2 || m.setup.values != original {
		t.Fatal("hjkl must navigate without typing in Normal")
	}
	next, _ := m.Update(tea.PasteMsg{Content: "must not type"})
	m = next.(model)
	if m.setup.values != original {
		t.Fatal("Normal mode must ignore paste")
	}
	m = press(m, "G")
	if m.setup.focus != setupFields+1 {
		t.Fatal("G must jump to the final action")
	}
	m = press(m, "g")
	m = special(m, tea.KeyEscape)
	if m.setup == nil || m.setup.pending != "" {
		t.Fatal("Esc must cancel a pending navigation key")
	}
	m = press(m, "ggl")
	if m.setup.focus != 0 || m.setup.values[0] != "Gmail" || m.setup.insert {
		t.Fatal("gg and l must navigate the provider selector")
	}
	m = press(m, "jjI")
	if !m.setup.insert || m.setup.cursor != 0 || m.View().Cursor.Shape != tea.CursorBar {
		t.Fatal("I must enter Insert at the beginning of the field")
	}
	lines := strings.Split(ansi.Strip(m.View().Content), "\n")
	if !strings.Contains(lines[len(lines)-2], "Insert") || !strings.Contains(lines[len(lines)-1], "Esc normal") {
		t.Fatal("application footer must follow account modal mode")
	}
	m = control(m, 'u')
	m = press(m, "jk@example.com")
	if m.setup.values[2] != "jk@example.com" {
		t.Fatal("j/k must type inside Insert mode")
	}
	m = special(m, tea.KeyEscape)
	if m.setup == nil || m.setup.insert || m.setup.values[2] != "jk@example.com" || m.View().Cursor.Shape != tea.CursorBlock {
		t.Fatal("Esc must leave editing without dismissing or losing text")
	}
	m = press(m, "ji")
	m = special(m, tea.KeyTab)
	if m.setup.insert || m.setup.focus != 4 {
		t.Fatal("changing fields must return to Normal")
	}
	host := m.setup.values[4]
	m = press(m, "jk")
	if m.setup.values[4] != host || m.setup.focus != 4 {
		t.Fatal("navigation after Tab must not change the next field")
	}
	m = special(m, tea.KeyEnter)
	if !m.setup.insert {
		t.Fatal("Enter on a text field must start editing")
	}
	m = special(m, tea.KeyEnter)
	if m.setup.insert || m.setup.focus != 5 {
		t.Fatal("Enter while editing must finish and advance in Normal")
	}
	m = press(m, "q")
	if m.setup != nil || m.quitOpen {
		t.Fatal("q in Normal must dismiss only the modal")
	}
}
