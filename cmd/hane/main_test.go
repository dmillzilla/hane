package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/dmillzilla/hane/internal/app"
	"github.com/dmillzilla/hane/internal/buildinfo"
)

func TestDispatchesExactlyOneAction(t *testing.T) {
	for _, tc := range []struct {
		args, action string
		options      app.Options
		account      string
	}{
		{"", "open", app.Options{}, ""},
		{"--offline --no-splash", "open", app.Options{Offline: true, NoSplash: true}, ""},
		{"--no-splash demo", "open", app.Options{Demo: true, NoSplash: true}, ""},
		{"-d", "open", app.Options{Demo: true}, ""},
		{"sync -a me@example.com", "sync", app.Options{}, "me@example.com"},
		{"--account=me@example.com check", "check", app.Options{}, "me@example.com"},
		{"passwords -a me@example.com", "passwords", app.Options{}, "me@example.com"},
		{"--sync-once --account me@example.com", "sync", app.Options{}, "me@example.com"},
		{"update", "update", app.Options{}, ""},
	} {
		t.Run(tc.args, func(t *testing.T) {
			var out, errout bytes.Buffer
			calls := 0
			record := func(action string, options app.Options, account string) error {
				calls++
				if action != tc.action || options != tc.options || account != tc.account {
					t.Fatalf("unexpected action %s, options %+v, account %q", action, options, account)
				}
				return nil
			}
			h := handlers{
				open:      func(o app.Options) error { return record("open", o, "") },
				sync:      func(a string) error { return record("sync", app.Options{}, a) },
				check:     func(a string) error { return record("check", app.Options{}, a) },
				passwords: func(a string) error { return record("passwords", app.Options{}, a) },
				update: func(ctx context.Context, version string, writer io.Writer) error {
					if version != buildinfo.Version || writer != &out {
						t.Fatal("updater did not receive the output or version")
					}
					return record("update", app.Options{}, "")
				},
			}
			if code := run(context.Background(), strings.Fields(tc.args), &out, &errout, h); code != 0 || calls != 1 || errout.Len() != 0 {
				t.Fatalf("exit=%d, calls=%d, stderr=%s", code, calls, errout.String())
			}
		})
	}
}

func TestVersionHelpAndInvalidInputNeverOpenAccounts(t *testing.T) {
	// Nil handlers panic if any action is called, including mail or update work.
	for _, tc := range []struct {
		args string
		code int
	}{
		{"version", 0}, {"-v", 0}, {"--version", 0},
		{"help", 0}, {"help sync", 0}, {"demo --help", 0},
		{"update --help", 0}, {"passwords --help", 0},
		{"sync --offline", 2}, {"demo version", 2},
		{"--account=", 2}, {"--unexpected", 2},
	} {
		t.Run(tc.args, func(t *testing.T) {
			var out, errout bytes.Buffer
			if code := run(context.Background(), strings.Fields(tc.args), &out, &errout, handlers{}); code != tc.code {
				t.Fatalf("exit=%d, want %d; stderr=%s", code, tc.code, errout.String())
			}
		})
	}
}

func TestActionFailureHasNonzeroExit(t *testing.T) {
	var out, errout bytes.Buffer
	h := handlers{check: func(string) error { return errors.New("connection failed") }}
	if code := run(context.Background(), []string{"check"}, &out, &errout, h); code != 1 || !strings.Contains(errout.String(), "connection failed") {
		t.Fatalf("exit=%d, stderr=%s", code, errout.String())
	}
}
