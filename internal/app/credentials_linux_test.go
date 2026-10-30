//go:build linux

package app

import (
	"os"
	"path/filepath"
	"testing"
)

func TestRejectUnprotectedGnomeKeyring(t *testing.T) {
	p := filepath.Join(t.TempDir(), "test.keyring")
	for _, fixture := range []string{"[keyring]\ndisplay-name=Hane\n", "", "broken"} {
		if err := os.WriteFile(p, []byte(fixture), 0600); err != nil {
			t.Fatal(err)
		}
		if encryptedKeyringFile(p) == nil {
			t.Fatal("unprotected or unknown keyring must be rejected")
		}
	}
	if err := os.WriteFile(p, []byte("GnomeKeyring\n\r\x00\n\x00"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := encryptedKeyringFile(p); err != nil {
		t.Fatal(err)
	}
}
