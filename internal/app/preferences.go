package app

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
)

type readerPreferences struct {
	Version      int  `json:"version"`
	RemoteImages bool `json:"remote_images"`
}

func preferencesPath() (string, error) {
	root, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(root, storageNamespace, "preferences.json"), nil
}

func readPreferences(path string) (readerPreferences, error) {
	p := readerPreferences{Version: 1}
	f, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return p, nil
	}
	if err != nil {
		return p, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || info.Size() > 64<<10 {
		return readerPreferences{Version: 1}, errors.New("preferences.json exceeds its size limit or could not be read")
	}
	d := json.NewDecoder(io.LimitReader(f, 64<<10))
	if err := d.Decode(&p); err != nil {
		return readerPreferences{Version: 1}, errors.New("could not read preferences.json")
	}
	var suffix any
	if err := d.Decode(&suffix); err != io.EOF || p.Version != 1 {
		return readerPreferences{Version: 1}, errors.New("unsupported or invalid preferences.json")
	}
	return p, nil
}

func (m *model) loadPreferences() {
	path, err := preferencesPath()
	if err == nil {
		m.preferencesPath = path
		m.preferences, err = readPreferences(path)
	}
	if err != nil {
		m.preferences = readerPreferences{Version: 1}
		m.preferencesReadError = "Reader preferences could not be loaded. Fix preferences.json before changing settings."
	}
}

func (m *model) setRemoteImages(enabled bool) error {
	if m.preferencesReadError != "" {
		return errors.New(m.preferencesReadError)
	}
	p := m.preferences
	p.Version, p.RemoteImages = 1, enabled
	if !m.demo && m.preferencesPath != "" {
		if err := atomicPrivateJSON(m.preferencesPath, p); err != nil {
			return errors.New("Could not save reading preferences. Check the config directory permissions.")
		}
	}
	m.preferences = p
	m.settingsError = ""
	if !enabled && m.images != nil {
		m.cancelImages()
		m.images.visible = map[string]bool{}
	}
	m.clampScroll()
	return nil
}

func (m *model) openSettings() {
	m.settingsOpen, m.settingsError, m.prompt, m.input = true, "", "", ""
}

func (m model) handleSettings(msg tea.Msg) (tea.Model, tea.Cmd) {
	enabled, change := m.preferences.RemoteImages, false
	switch key := msg.(type) {
	case tea.KeyPressMsg:
		switch key.String() {
		case "q", "esc", "ctrl+c":
			m.settingsOpen = false
			return m, nil
		case "enter", " ":
			enabled, change = !enabled, true
		case "h", "left":
			enabled, change = false, true
		case "l", "right":
			enabled, change = true, true
		}
	case tea.MouseClickMsg:
		if key.Button == tea.MouseLeft && key.Y == 6 {
			enabled, change = !enabled, true
		}
	}
	if change {
		if err := m.setRemoteImages(enabled); err != nil {
			m.settingsError = err.Error()
			m.notice = err.Error()
		} else {
			m.notice = "Reading preferences saved."
			if m.demo {
				m.notice = "Demo reading preference changed for this session."
			}
		}
	}
	return m, nil
}

func (m model) settingsView() string {
	check := " "
	if m.preferences.RemoteImages {
		check = "x"
	}
	lines := []string{
		bold(" Reading", cyan), "",
		bold(fmt.Sprintf(" [%s] Always show remote images", check), teal), "",
	}
	if m.preferencesReadError != "" {
		lines = append(lines, wrappedSetting(m.preferencesReadError, max(1, m.width-4))...)
	}
	if m.settingsError != "" {
		lines = append(lines, wrappedSetting(m.settingsError, max(1, m.width-4))...)
	}
	details := []string{
		"Images load automatically as you read after this is enabled.",
		"This applies to all accounts and is remembered between launches.",
		"Image hosts can see when you open a message. Known hidden and tracking-sized images are skipped.",
		"Embedded and attached image previews load when you open a message.",
	}
	if m.demo {
		details = []string{
			"Try the image button in 'A few photos from the coast' in Personal / Inbox.",
			"Demo images come from local samples. No image host is contacted.",
			"This setting applies to this demo session only. Your mailbox preference stays unchanged.",
			"Each demo launch starts with remote image samples hidden. Embedded images still load automatically.",
		}
	}
	for _, text := range details {
		lines = append(lines, wrappedSetting(text, max(1, m.width-4))...)
		lines = append(lines, "")
	}
	return paneLines(lines, m.width, m.bodyHeight(), false, bluePane)
}

func wrappedSetting(text string, width int) []string {
	var out []string
	for _, line := range strings.Split(ansi.Wrap(text, width, ""), "\n") {
		out = append(out, styled(" "+line, muted))
	}
	return out
}
