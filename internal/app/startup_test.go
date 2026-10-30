package app

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestMailStorageLockIsSharedAndReleased(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	first, second := newModel(time.Now()), newModel(time.Now())
	unlock, err := first.openMailStorage()
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()
	if _, err := second.openMailStorage(); err == nil || !strings.Contains(err.Error(), "Another Hane instance") {
		t.Fatalf("second instance did not respect the mail lock: %v", err)
	}
	unlock()
	unlockSecond, err := second.openMailStorage()
	if err != nil {
		t.Fatalf("closing the first instance did not release the lock: %v", err)
	}
	unlockSecond()
}

func TestCorruptMailStartupReleasesLockAndPreservesFile(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	path, err := mailDataPath()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("corrupt cache"), 0600); err != nil {
		t.Fatal(err)
	}
	m := newModel(time.Now())
	if _, err := m.openMailStorage(); err == nil {
		t.Fatal("corrupt mail storage was accepted")
	}
	data, err := os.ReadFile(path)
	if err != nil || string(data) != "corrupt cache" {
		t.Fatal("startup changed corrupt storage")
	}
	if err := saveMailDisk(path, mailDisk{Version: 1, Accounts: map[string]accountMail{}}); err != nil {
		t.Fatal(err)
	}
	unlock, err := m.openMailStorage()
	if err != nil {
		t.Fatalf("startup failure left the mail store locked: %v", err)
	}
	unlock()
}
