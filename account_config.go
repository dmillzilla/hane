package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/mail"
	"os"
	"path/filepath"
	"strings"
	"unicode"
)

type AccountConfig struct {
	RememberPassword bool     `json:"remember_password,omitempty"`
	BitwardenItem    string   `json:"bitwarden_item,omitempty"`
	Name             string   `json:"name"`
	Email            string   `json:"email"`
	Provider         string   `json:"provider"`
	IMAPHost         string   `json:"imap_host"`
	IMAPPort         int      `json:"imap_port"`
	IMAPUser         string   `json:"imap_user"`
	SMTPHost         string   `json:"smtp_host"`
	SMTPPort         int      `json:"smtp_port"`
	SMTPUser         string   `json:"smtp_user"`
	SMTPSecurity     string   `json:"smtp_security"`
	Folders          []string `json:"folders"`
}

type accountFile struct {
	Version  int             `json:"version"`
	Accounts []AccountConfig `json:"accounts"`
}

func accountConfigPath() (string, error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, storageNamespace, "accounts.json"), nil
}

func validHost(s string) bool {
	if net.ParseIP(s) != nil {
		return true
	}
	if s == "" || len(s) > 253 {
		return false
	}
	for _, label := range strings.Split(s, ".") {
		if label == "" || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}
		for _, r := range label {
			if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-') {
				return false
			}
		}
	}
	return true
}
func safeField(s string) bool {
	return len(s) <= 1024 && !strings.ContainsFunc(s, unicode.IsControl)
}
func (c AccountConfig) validate() (int, string) {
	if strings.TrimSpace(c.Name) == "" || !safeField(c.Name) {
		return 1, "Enter an account name."
	}
	email, err := mail.ParseAddress(c.Email)
	if err != nil || email.Address != c.Email || !strings.Contains(c.Email, "@") || !safeField(c.Email) {
		return 2, "Enter a complete email address."
	}
	if !validHost(c.IMAPHost) {
		return 4, "IMAP host: use a hostname, without a URL or port."
	}
	if c.IMAPPort < 1 || c.IMAPPort > 65535 {
		return 5, "IMAP port must be between 1 and 65535."
	}
	if c.IMAPUser == "" || !safeField(c.IMAPUser) {
		return 6, "Enter the IMAP sign-in name."
	}
	if !validHost(c.SMTPHost) {
		return 7, "SMTP host: use a hostname, without a URL or port."
	}
	if c.SMTPPort < 1 || c.SMTPPort > 65535 {
		return 8, "SMTP port must be between 1 and 65535."
	}
	if c.SMTPUser == "" || !safeField(c.SMTPUser) {
		return 9, "Enter the SMTP sign-in name."
	}
	if c.SMTPSecurity != "STARTTLS" && c.SMTPSecurity != "TLS" {
		return 10, "SMTP security must be STARTTLS or TLS."
	}
	if c.Provider != "iCloud" && c.Provider != "Gmail" && c.Provider != "Custom" {
		return 0, "Choose iCloud, Gmail, or Custom."
	}
	return -1, ""
}
func loadAccountConfigs(path string) ([]AccountConfig, error) {
	f, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()
	dec := json.NewDecoder(io.LimitReader(f, 1<<20))
	var file accountFile
	if err = dec.Decode(&file); err != nil {
		return nil, fmt.Errorf("cannot read account settings: %w", err)
	}
	var extra any
	if err = dec.Decode(&extra); err != io.EOF {
		return nil, errors.New("unexpected data after account settings")
	}
	if file.Version != 1 {
		return nil, errors.New("unsupported account settings version")
	}
	seen := map[string]bool{}
	for _, c := range file.Accounts {
		if _, msg := c.validate(); msg != "" {
			return nil, fmt.Errorf("invalid saved account: %s", msg)
		}
		key := strings.ToLower(c.Email)
		if seen[key] {
			return nil, errors.New("duplicate email in account settings")
		}
		seen[key] = true
		for _, folder := range c.Folders {
			if !safeField(folder) {
				return nil, errors.New("invalid saved folder name")
			}
		}
	}
	return file.Accounts, nil
}
func saveAccountConfigs(path string, configs []AccountConfig) error {
	if path == "" {
		return errors.New("account settings location unavailable")
	}
	data, err := json.MarshalIndent(accountFile{1, configs}, "", "  ")
	if err != nil {
		return err
	}
	dir := filepath.Dir(path)
	if err = os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	f, err := os.CreateTemp(dir, ".accounts-*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	defer os.Remove(tmp)
	if err = f.Chmod(0600); err == nil {
		_, err = f.Write(append(data, '\n'))
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
	return os.Rename(tmp, path)
}
func (m *model) loadAccounts() {
	path, err := accountConfigPath()
	m.configPath = path
	if err != nil {
		m.configError = "Cannot locate account settings."
		m.notice = m.configError
		return
	}
	configs, err := loadAccountConfigs(path)
	if err != nil {
		m.configError = "Account settings could not be loaded. Fix accounts.json before adding accounts."
		m.notice = m.configError
		return
	}
	for _, c := range configs {
		m.appendConfiguredAccount(c, false)
		if c.RememberPassword {
			m.accounts[len(m.accounts)-1].ConnectionStatus = "Checking sign-in with saved password…"
		}
	}
}
func (m *model) appendConfiguredAccount(c AccountConfig, verified bool) {
	folders := append([]string(nil), c.Folders...)
	if len(folders) == 0 {
		folders = []string{"INBOX"}
	}
	m.accounts = append(m.accounts, Account{Name: c.Name, Address: c.Email, Folders: folders, Config: &c, Verified: verified})
	m.expanded = append(m.expanded, true)
}
