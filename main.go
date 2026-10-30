package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/gofrs/flock"
)

const appName = "Hane"
const version = "0.4.3"

// Keep the original storage namespace so upgrades share settings, mail, and locks.
const storageNamespace = "sorakyaku"

func main() {
	if len(os.Args) > 1 && os.Args[1] == "__keyring" {
		os.Exit(runKeyringHelper(os.Args[2:]))
	}
	offline := flag.Bool("offline", false, "read cached mail and edit drafts without background network access")
	demo := flag.Bool("demo", false, "use fictional mail without network or persistent storage")
	checkConnection := flag.Bool("check-connection", false, "verify IMAP and SMTP sign-in without sending mail")
	syncOnce := flag.Bool("sync-once", false, "sync configured mail once and print counts only")
	noSplash := flag.Bool("no-splash", false, "skip the opening animation")
	passwords := flag.Bool("password-setup", false, "set up or refresh a Bitwarden-backed mail password")
	account := flag.String("account", "", "email account for password setup or connection checks")
	showVersion := flag.Bool("version", false, "print version")
	flag.Parse()
	if *showVersion {
		fmt.Println(appName, version)
		return
	}
	if *passwords {
		err := passwordSetup(*account)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
		}
		_, _ = promptLine("Press Enter to return…")
		if err != nil {
			os.Exit(1)
		}
		return
	}
	m := newModel(time.Now())
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	m.background = ctx
	m.credentials = systemCredentials{}
	if !*demo {
		m.loadAccounts()
		if *checkConnection {
			if err := m.verifyMailConnection(*account); err != nil {
				fmt.Fprintln(os.Stderr, err)
				os.Exit(1)
			}
			return
		}
		path, err := mailDataPath()
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return
		}
		if err = os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			fmt.Fprintln(os.Stderr, err)
			return
		}
		lock := flock.New(path + ".lock")
		ok, err := lock.TryLock()
		if err != nil || !ok {
			fmt.Fprintln(os.Stderr, "Another Hane instance is using mail storage. Close it before starting this one.")
			return
		}
		defer lock.Unlock()
		if err = m.initLive(path); err != nil {
			fmt.Fprintln(os.Stderr, err)
			return
		}
		m.live.offline = *offline
		if *offline {
			m.notice = "Offline · cached mail and local drafts"
		}
		if *syncOnce && *offline {
			fmt.Fprintln(os.Stderr, "--sync-once cannot be used with --offline")
			return
		}
		if *syncOnce {
			if err := m.syncOnce(*account); err != nil {
				fmt.Fprintln(os.Stderr, err)
				os.Exit(1)
			}
			return
		}
	}
	m.loading = !*noSplash
	if _, err := tea.NewProgram(m).Run(); err != nil {
		cancel()
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
