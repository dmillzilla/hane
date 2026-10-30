package app

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/dmillzilla/hane/internal/buildinfo"

	tea "charm.land/bubbletea/v2"
	"github.com/gofrs/flock"
)

const appName = "Hane"

var version = buildinfo.Version

// Keep the original storage namespace so upgrades share settings, mail, and locks.
const storageNamespace = "sorakyaku"

// Options modify the interactive mailbox. Actions are dispatched by the CLI.
type Options struct {
	Demo, Offline, NoSplash bool
}

// Run opens the terminal client with already validated options.
func Run(options Options) error {
	m := newModel(time.Now())
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	m.background = ctx
	m.credentials = systemCredentials{}
	if !options.Demo {
		m.loadAccounts()
		unlock, err := m.openMailStorage()
		if err != nil {
			return err
		}
		defer unlock()
		m.live.offline = options.Offline
		if options.Offline {
			m.notice = "Offline · cached mail and local drafts"
		}
	}
	m.loading = !options.NoSplash
	_, err := tea.NewProgram(m).Run()
	return err
}

// Sync synchronizes matching configured accounts once, without opening the UI.
func Sync(email string) error {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	m := newModel(time.Now())
	m.background, m.credentials = ctx, systemCredentials{}
	m.loadAccounts()
	unlock, err := m.openMailStorage()
	if err != nil {
		return err
	}
	defer unlock()
	return m.syncOnce(email)
}

// Check verifies matching configured accounts without opening the mail store.
func Check(email string) error {
	m := newModel(time.Now())
	m.credentials = systemCredentials{}
	m.loadAccounts()
	return m.verifyMailConnection(email)
}

// Passwords runs the interactive credential setup wizard.
func Passwords(email string) error { return passwordSetup(email) }

// Keyring is the private subprocess entry point used for bounded keyring calls.
func Keyring(args []string) int { return runKeyringHelper(args) }

func (m *model) openMailStorage() (func(), error) {
	path, err := mailDataPath()
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return nil, err
	}
	lock := flock.New(path + ".lock")
	ok, err := lock.TryLock()
	if err != nil {
		return nil, fmt.Errorf("lock mail storage: %w", err)
	}
	if !ok {
		return nil, errors.New("Another Hane instance is using mail storage. Close it before starting this one.")
	}
	if err := m.initLive(path); err != nil {
		_ = lock.Unlock()
		return nil, err
	}
	return func() { _ = lock.Unlock() }, nil
}
