package app

import (
	"context"
	"image"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
)

func TestRemoteImagePreferencePersistsAcrossLaunches(t *testing.T) {
	root := t.TempDir()
	t.Setenv("HOME", root)
	t.Setenv("XDG_CONFIG_HOME", root)
	m := remoteImageModel(t)
	m.loadPreferences()
	if m.preferences.RemoteImages {
		t.Fatal("remote images must be off without a saved preference")
	}
	m.images.testLoader = func(context.Context, imageJob) (image.Image, int64, error) { return demoPostcard(), 100, nil }
	m, cmd := runImageKey(m)
	if cmd == nil {
		t.Fatal("Always show did not schedule the image")
	}
	info, err := os.Stat(m.preferencesPath)
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatalf("preference was not saved privately: %v", err)
	}
	if dir, err := os.Stat(filepath.Dir(m.preferencesPath)); err != nil || dir.Mode().Perm() != 0700 {
		t.Fatalf("preference directory was not private: %v", err)
	}
	restarted := remoteImageModel(t)
	restarted.loadPreferences()
	restarted.images.testLoader = m.images.testLoader
	next, cmd := restarted.Update(tea.WindowSizeMsg{Width: 120, Height: 36})
	restarted = next.(model)
	if !restarted.preferences.RemoteImages || cmd == nil || restarted.blockedRemoteImages() {
		t.Fatal("next launch forgot the preference or required another action")
	}
	restarted = press(restarted, "Sh")
	saved, err := readPreferences(restarted.preferencesPath)
	if err != nil || saved.RemoteImages {
		t.Fatal("Settings did not save the disabled preference")
	}
}

func TestPreferenceSaveFailureDoesNotEnableRemoteRequests(t *testing.T) {
	m := remoteImageModel(t)
	parent := filepath.Join(t.TempDir(), "not-a-directory")
	if err := os.WriteFile(parent, []byte("preserve"), 0600); err != nil {
		t.Fatal(err)
	}
	m.preferencesPath = filepath.Join(parent, "preferences.json")
	m, cmd := runImageKey(m)
	if cmd != nil || m.preferences.RemoteImages || !m.blockedRemoteImages() || !strings.Contains(m.notice, "Could not save") {
		t.Fatal("save failure enabled remote images or was not explained")
	}
}

func TestDemoPreferenceDoesNotOverwriteMailboxPreference(t *testing.T) {
	path := filepath.Join(t.TempDir(), "preferences.json")
	const saved = `{"version":1,"remote_images":false}`
	if err := os.WriteFile(path, []byte(saved), 0600); err != nil {
		t.Fatal(err)
	}
	m := newModel(time.Now())
	m.demo, m.preferencesPath = true, path
	if err := m.setRemoteImages(true); err != nil || !m.preferences.RemoteImages {
		t.Fatalf("demo setting did not change for this session: %v", err)
	}
	got, err := os.ReadFile(path)
	if err != nil || string(got) != saved {
		t.Fatal("demo setting modified the saved mailbox preference")
	}
}

func TestInvalidPreferencesRemainBlockedAndPreserved(t *testing.T) {
	for _, input := range []string{
		`{"version":1,"remote_images":true} trailing`,
		`{"version":2,"remote_images":true}`,
		`{"version":1,"remote_images":"true"}`,
		`{"version":1,"remote_images":true}` + strings.Repeat(" ", 64<<10),
	} {
		t.Run(input[:min(40, len(input))], func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "preferences.json")
			if err := os.WriteFile(path, []byte(input), 0600); err != nil {
				t.Fatal(err)
			}
			p, err := readPreferences(path)
			if err == nil || p.RemoteImages {
				t.Fatal("invalid preferences enabled remote images")
			}
			m := newModel(time.Now())
			m.preferences, m.preferencesPath, m.preferencesReadError = p, path, "Invalid preferences preserved"
			if err := m.setRemoteImages(true); err == nil || m.preferences.RemoteImages {
				t.Fatal("unreadable preferences were silently replaced")
			}
			got, err := os.ReadFile(path)
			if err != nil || string(got) != input {
				t.Fatal("invalid preferences were not preserved")
			}
		})
	}
}

func TestReadingSettingsLayoutAndCommand(t *testing.T) {
	m := newModel(time.Now())
	m = press(m, ":settings")
	m = special(m, tea.KeyEnter)
	if !m.settingsOpen || !strings.Contains(ansi.Strip(m.View().Content), "Always show remote images") {
		t.Fatal("reading settings command did not open the preference")
	}
	for _, size := range [][2]int{{120, 36}, {90, 24}, {48, 16}} {
		next, _ := m.Update(tea.WindowSizeMsg{Width: size[0], Height: size[1]})
		m = next.(model)
		lines := strings.Split(m.View().Content, "\n")
		if len(lines) != size[1] {
			t.Fatal("settings exceeded terminal height")
		}
		for _, line := range lines {
			if ansi.StringWidth(line) != size[0] {
				t.Fatal("settings exceeded terminal width")
			}
		}
	}
}
