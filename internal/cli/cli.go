// Package cli parses commands and their composable options without starting Hane.
package cli

import (
	"fmt"
	"io"
	"net/mail"
	"strconv"
	"strings"
)

type Command string

const (
	Open      Command = ""
	Demo      Command = "demo"
	Version   Command = "version"
	Update    Command = "update"
	Sync      Command = "sync"
	Check     Command = "check"
	Passwords Command = "passwords"
)

type Invocation struct {
	Command           Command
	Account           string
	Offline, NoSplash bool
	Help              bool
}

var commands = map[string]Command{
	"demo": Demo, "version": Version, "update": Update,
	"sync": Sync, "check": Check, "passwords": Passwords,
	// Keep names from the old action flags usable as command aliases too.
	"sync-once": Sync, "check-connection": Check, "password-setup": Passwords,
}

// Parse accepts options on either side of a command, but never combines actions.
// Legacy action flags remain aliases so scripts written for v0.5 keep working.
func Parse(args []string) (Invocation, error) {
	var result Invocation
	var selected Command
	setCommand := func(command Command) error {
		if selected != Open && selected != command {
			return fmt.Errorf("choose one command: %s and %s cannot run together", selected, command)
		}
		selected, result.Command = command, command
		return nil
	}
	options := true
	accountSet, offlineSet, splashSet := false, false, false
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if options && arg == "--" {
			options = false
			continue
		}
		if options && strings.HasPrefix(arg, "-") && arg != "-" {
			name, value, hasValue := strings.Cut(strings.TrimPrefix(strings.TrimPrefix(arg, "-"), "-"), "=")
			if name == "account" || name == "a" {
				if !hasValue {
					if i+1 >= len(args) || strings.HasPrefix(args[i+1], "-") {
						return result, fmt.Errorf("%s needs an email address", arg)
					}
					i++
					value = args[i]
				}
				address, err := mail.ParseAddress(value)
				if err != nil || address.Address != value || !strings.Contains(value, "@") {
					return result, fmt.Errorf("--account needs a complete email address")
				}
				if accountSet && !strings.EqualFold(result.Account, value) {
					return result, fmt.Errorf("choose one --account per invocation")
				}
				result.Account, accountSet = value, true
				continue
			}
			// Boolean options also accept =true or =false, as in the old CLI.
			enabled := true
			if hasValue {
				var err error
				enabled, err = strconv.ParseBool(value)
				if err != nil {
					return result, fmt.Errorf("%s expects true or false", arg)
				}
			}
			var action Command
			switch name {
			case "h", "help":
				result.Help = enabled
			case "o", "offline":
				result.Offline, offlineSet = enabled, true
			case "no-splash":
				result.NoSplash, splashSet = enabled, true
			case "d", "demo":
				action = Demo
			case "v", "version":
				action = Version
			case "sync-once":
				action = Sync
			case "check-connection":
				action = Check
			case "password-setup":
				action = Passwords
			default:
				return result, fmt.Errorf("unknown option %q", arg)
			}
			if action != Open && enabled {
				if err := setCommand(action); err != nil {
					return result, err
				}
			}
			continue
		}
		if arg == "help" {
			result.Help = true
			continue
		}
		command, ok := commands[arg]
		if !ok {
			return result, fmt.Errorf("unknown command %q", arg)
		}
		if err := setCommand(command); err != nil {
			return result, err
		}
	}
	if result.Help {
		return result, nil
	}
	if offlineSet && result.Command != Open {
		return result, fmt.Errorf("--offline is an option for opening your mailbox: hane --offline")
	}
	if splashSet && result.Command != Open && result.Command != Demo {
		return result, fmt.Errorf("--no-splash applies only to hane and hane demo")
	}
	if accountSet && result.Command != Sync && result.Command != Check && result.Command != Passwords {
		return result, fmt.Errorf("--account applies to hane sync, hane check, and hane passwords")
	}
	return result, nil
}

// Help shows only options that apply to the selected command.
func Help(out io.Writer, command Command) {
	switch command {
	case Demo:
		fmt.Fprintln(out, "Usage: hane demo [options]\n\nOpen sample mail without network connections or persistent storage.\n\nOptions:\n  --no-splash  Skip the opening animation\n  -h, --help   Show help\n\nAlias: -d, --demo")
	case Version:
		fmt.Fprintln(out, "Usage: hane version\n\nPrint the installed Hane version.\n\nAliases: -v, --version")
	case Update:
		fmt.Fprintln(out, "Usage: hane update\n\nDownload and install the latest stable Hane release for this computer.")
	case Sync, Check, Passwords:
		description := map[Command]string{
			Sync:      "Sync configured mail once and print counts. Does not submit outgoing messages.",
			Check:     "Verify IMAP and SMTP sign-in without sending mail.",
			Passwords: "Set up or refresh a Bitwarden-backed mail password in an interactive terminal.",
		}
		fmt.Fprintf(out, "Usage: hane %s [options]\n\n%s\n\nOptions:\n  -a, --account EMAIL  Use one configured account", command, description[command])
		if command == Passwords {
			fmt.Fprintln(out, " (otherwise choose interactively)")
		} else {
			fmt.Fprintln(out, " (otherwise use all accounts)")
		}
		fmt.Fprintln(out, "  -h, --help           Show help")
	default:
		fmt.Fprintln(out, `Usage: hane [options]
       hane <command> [options]

Open your mailbox, or run one command:
  demo       Open sample mail (-d, --demo)
  version    Print the installed version (-v, --version)
  update     Install the latest stable release
  sync       Sync mail once without submitting outgoing messages
  check      Check IMAP and SMTP sign-in
  passwords  Set up or refresh remembered passwords
  help       Show help, or use hane help <command>

Mailbox options:
  -o, --offline  Read cached mail and write drafts without connecting
  --no-splash   Skip the opening animation (also works with demo)
  -h, --help    Show help

Account commands accept -a, --account EMAIL.
Options can appear before or after the command.

Examples:
  hane --offline --no-splash
  hane demo --no-splash
  hane sync -a me@example.com
  hane --account me@example.com check`)
	}
}
