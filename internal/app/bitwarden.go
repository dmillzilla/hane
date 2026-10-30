package app

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"golang.org/x/term"
)

var bitwardenID = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

type bitwardenClient struct{ executable, directory, session string }
type vaultItem struct {
	ID    string `json:"id,omitempty"`
	Type  int    `json:"type"`
	Name  string `json:"name"`
	Notes string `json:"notes"`
	Login struct {
		Username string `json:"username"`
		Password string `json:"password"`
	} `json:"login"`
}

func findBitwarden() (string, error) {
	if p, err := exec.LookPath("bw"); err == nil {
		return p, nil
	}
	home, _ := os.UserHomeDir()
	p := filepath.Join(home, ".local", "share", storageNamespace, "bitwarden-cli", "node_modules", ".bin", "bw")
	if _, err := os.Stat(p); err == nil {
		return p, nil
	}
	return "", errors.New("Install the Bitwarden CLI (bw), then retry password setup.")
}

func (b *bitwardenClient) command(ctx context.Context, input io.Reader, interactive bool, args ...string) ([]byte, error) {
	timeout := 60 * time.Second
	if interactive {
		timeout = 10 * time.Minute
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, b.executable, args...)
	// Isolate Hane's encrypted CLI state and session from other CLI uses.
	for _, env := range os.Environ() {
		if !strings.HasPrefix(env, "BW_") && !strings.HasPrefix(env, "BITWARDENCLI_APPDATA_DIR=") {
			cmd.Env = append(cmd.Env, env)
		}
	}
	cmd.Env = append(cmd.Env, "BITWARDENCLI_APPDATA_DIR="+b.directory, "BW_SESSION="+b.session)
	cmd.Stdin = input
	if interactive {
		// Bitwarden's interactive login/2FA prompts go to stderr. --raw sends
		// only the session key to the private stdout pipe.
		cmd.Stdin, cmd.Stderr = os.Stdin, os.Stderr
	} else {
		cmd.Env = append(cmd.Env, "BW_NOINTERACTION=true")
	}
	data, err := cmd.Output()
	if err != nil {
		return nil, errors.New("Bitwarden could not complete the request. Check your sign-in, vault region, and network, then retry.")
	}
	return data, nil
}

func (b *bitwardenClient) unlock(ctx context.Context) error {
	data, err := b.command(ctx, nil, false, "status")
	if err != nil {
		return err
	}
	var status struct {
		Status string `json:"status"`
	}
	if json.Unmarshal(data, &status) != nil {
		return errors.New("Cannot read Bitwarden status.")
	}
	if status.Status == "unauthenticated" {
		fmt.Println("Sign into Bitwarden here. Create a free account in the Bitwarden app first if needed.")
		region, err := promptLine("Vault region: 1 = US, 2 = EU [1]: ")
		if err != nil {
			return err
		}
		server := "https://vault.bitwarden.com"
		if region == "2" {
			server = "https://vault.bitwarden.eu"
		} else if region != "" && region != "1" {
			return errors.New("Choose 1 or 2 for the vault region.")
		}
		if _, err = b.command(ctx, nil, false, "config", "server", server); err != nil {
			return err
		}
		data, err = b.command(ctx, nil, true, "login", "--raw")
	} else if status.Status == "locked" || status.Status == "unlocked" {
		fmt.Println("Unlock Bitwarden to copy your mail password to this device.")
		data, err = b.command(ctx, nil, true, "unlock", "--raw")
	} else {
		return errors.New("Unrecognized Bitwarden status.")
	}
	if err != nil {
		return err
	}
	session := strings.TrimSpace(string(data))
	decoded, err := base64.StdEncoding.DecodeString(session)
	if err != nil || len(decoded) < 32 {
		return errors.New("Bitwarden did not return a valid unlock session. Update the CLI and retry.")
	}
	b.session = session
	_, err = b.command(ctx, nil, false, "sync")
	return err
}

func (b *bitwardenClient) get(ctx context.Context, id string, c AccountConfig) (string, error) {
	if !bitwardenID.MatchString(id) {
		return "", errors.New("Enter the item's UUID from Bitwarden, not its name or password.")
	}
	data, err := b.command(ctx, nil, false, "get", "item", id)
	if err != nil {
		return "", err
	}
	var item vaultItem
	if json.Unmarshal(data, &item) != nil || !strings.EqualFold(item.ID, id) || item.Type != 1 {
		return "", errors.New("That Bitwarden item is not an available login.")
	}
	if !strings.EqualFold(item.Login.Username, c.Email) {
		return "", errors.New("The Bitwarden login username must match this account's email. Use a dedicated mail app-password item.")
	}
	if !validAppPassword(item.Login.Password) {
		return "", errors.New("The Bitwarden item has an empty or invalid app password.")
	}
	return normalizePassword(c, item.Login.Password), nil
}

func (b *bitwardenClient) create(ctx context.Context, c AccountConfig, password string) (string, error) {
	item := vaultItem{Type: 1, Name: "Hane · " + c.Email + " · App password", Notes: "Mail app password for Hane. Update this password here, then run hane --password-setup to refresh the local keyring copy."}
	item.Login.Username, item.Login.Password = c.Email, password
	data, _ := json.Marshal(item)
	encoded := base64.StdEncoding.EncodeToString(data)
	result, err := b.command(ctx, strings.NewReader(encoded), false, "create", "item")
	if err != nil {
		return "", errors.New("Bitwarden save was not confirmed. Check for the Hane item in your vault before retrying to avoid creating a duplicate.")
	}
	var saved vaultItem
	if json.Unmarshal(result, &saved) != nil || !bitwardenID.MatchString(saved.ID) {
		return "", errors.New("Bitwarden returned an unexpected result. Check for the Hane item in your vault before retrying.")
	}
	return saved.ID, nil
}

func validAppPassword(s string) bool {
	return strings.TrimSpace(s) != "" && len(s) <= 4096 && !strings.ContainsAny(s, "\x00\r\n")
}
func normalizePassword(c AccountConfig, s string) string {
	if c.Provider == "Gmail" {
		return strings.ReplaceAll(s, " ", "")
	}
	return s
}

// Read exactly one line; buffering ahead would consume Bitwarden's interactive
// input when we hand the terminal over for login or 2FA.
func promptLine(label string) (string, error) {
	fmt.Print(label)
	var b bytes.Buffer
	var ch [1]byte
	for b.Len() < 4096 {
		if _, err := os.Stdin.Read(ch[:]); err != nil {
			return "", err
		}
		if ch[0] == '\n' {
			return strings.TrimSpace(b.String()), nil
		}
		b.WriteByte(ch[0])
	}
	return "", errors.New("Input is too long.")
}

func passwordSetup(email string) error {
	if !term.IsTerminal(int(os.Stdin.Fd())) {
		return errors.New("Run password setup in an interactive terminal.")
	}
	path, err := accountConfigPath()
	if err != nil {
		return err
	}
	configs, err := loadAccountConfigs(path)
	if err != nil {
		return errors.New("Fix accounts.json before setting up passwords.")
	}
	if len(configs) == 0 {
		return errors.New("Add your email account in Hane first, then run password setup.")
	}
	index := -1
	if email != "" {
		for i, c := range configs {
			if strings.EqualFold(c.Email, email) {
				index = i
			}
		}
		if index < 0 {
			return errors.New("That email is not a configured account.")
		}
	} else if len(configs) == 1 {
		index = 0
	} else {
		for i, c := range configs {
			fmt.Printf("%d. %s (%s)\n", i+1, c.Name, c.Email)
		}
		line, err := promptLine("Account number: ")
		if err != nil {
			return err
		}
		n, err := strconv.Atoi(line)
		if err != nil || n < 1 || n > len(configs) {
			return errors.New("Choose a listed account number.")
		}
		index = n - 1
	}
	c := configs[index]
	fmt.Printf("\nHane %s · Password setup\nAccount: %s\nIMAP: %s:%d (%s)\nSMTP: %s:%d (%s)\n\n", version, c.Email, c.IMAPHost, c.IMAPPort, c.IMAPUser, c.SMTPHost, c.SMTPPort, c.SMTPUser)
	fmt.Println("Bitwarden holds the cloud copy. Your system keyring holds the local copy.")
	fmt.Println("This checks sign-in; the mail app handles reading and sending. Ctrl+C cancels.")
	fmt.Printf("On Linux, choose a non-empty password when the %s keyring dialog appears.\n", credentialCollectionName)
	fmt.Println()
	bw, err := findBitwarden()
	if err != nil {
		return err
	}
	dir := filepath.Join(filepath.Dir(path), "bitwarden-cli")
	if err = os.MkdirAll(dir, 0700); err != nil {
		return errors.New("Cannot create private Bitwarden CLI storage.")
	}
	if err = os.Chmod(dir, 0700); err != nil {
		return err
	}
	b := &bitwardenClient{executable: bw, directory: dir}
	ctx := context.Background()
	defer func() {
		lockCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_, _ = b.command(lockCtx, nil, false, "lock")
		b.session = ""
	}()
	if err = b.unlock(ctx); err != nil {
		return err
	}
	id := c.BitwardenItem
	if id == "" {
		fmt.Println("Use a login item containing your MAIL APP PASSWORD, with this email as its username.")
		id, err = promptLine("Existing Bitwarden item ID, or Enter to create one: ")
		if err != nil {
			return err
		}
	}
	var password string
	if id != "" {
		password, err = b.get(ctx, id, c)
		if err != nil {
			return err
		}
	} else {
		fmt.Print("Mail app password (hidden): ")
		secret, readErr := term.ReadPassword(int(os.Stdin.Fd()))
		fmt.Println()
		if readErr != nil {
			return errors.New("Cannot read app password from terminal.")
		}
		password = normalizePassword(c, string(secret))
		clear(secret)
		if !validAppPassword(password) {
			return errors.New("Enter a non-empty mail app password.")
		}
	}
	fmt.Println("Checking IMAP and SMTP sign-in…")
	folders, err := checkAccount(ctx, c, password)
	if err != nil {
		return err
	}
	if id == "" {
		fmt.Println("Saving the app password to Bitwarden…")
		id, err = b.create(ctx, c, password)
		if err != nil {
			return err
		}
	}
	fmt.Printf("Bitwarden item: %s\n", id)
	if err = (systemCredentials{}).Set(ctx, c, password); err != nil {
		return errors.New("The password is in Bitwarden, but local storage failed. Retry using the item ID above; create or unlock the Sorakyaku keyring with a non-empty password when prompted.")
	}
	c.RememberPassword, c.BitwardenItem, c.Folders = true, id, folders
	configs[index] = c
	if err = saveAccountConfigs(path, configs); err != nil {
		return errors.New("Bitwarden and the local keyring have the password, but settings could not be saved. Fix config permissions and retry using the item ID above.")
	}
	fmt.Println("Ready. Hane will check sign-in using your local keyring on launch.")
	fmt.Println("After changing this password in Bitwarden, run password setup again to refresh it.")
	return nil
}

type passwordSetupDone struct{ err error }

func (m model) launchPasswordSetup() (tea.Model, tea.Cmd) {
	index := m.account
	if m.focus == treePane {
		index = m.rows()[m.treeCursor].account
	}
	if index < 0 || m.accounts[index].Config == nil {
		m.notice = "Select a configured account, then press B for password setup."
		return m, nil
	}
	self, err := os.Executable()
	if err != nil {
		m.notice = "Cannot launch password setup."
		return m, nil
	}
	cmd := exec.Command(self, "--password-setup", "--account", m.accounts[index].Address)
	return m, tea.ExecProcess(cmd, func(err error) tea.Msg { return passwordSetupDone{err} })
}

func (m model) finishPasswordSetup(msg passwordSetupDone) (tea.Model, tea.Cmd) {
	configs, err := loadAccountConfigs(m.configPath)
	if err != nil {
		m.notice = "Password setup finished; restart after fixing account settings."
		return m, nil
	}
	for i := range m.accounts {
		a := &m.accounts[i]
		if a.Config == nil {
			continue
		}
		for _, c := range configs {
			if strings.EqualFold(a.Address, c.Email) {
				a.Config = &c
				if c.RememberPassword {
					a.ConnectionStatus = "Checking sign-in with saved password…"
					a.Verified = false
				}
			}
		}
	}
	m.notice = "Password setup finished."
	if msg.err != nil {
		m.notice = "Password setup was cancelled or failed; previous settings retained."
	}
	if m.live != nil {
		for i := range m.accounts {
			m.queueSync(i, syncRequest{Folder: inboxName(m.accounts[i])})
		}
		return m, nil
	}
	return m, m.reconnectAccounts()
}
