package cli

import (
	"bytes"
	"strings"
	"testing"
)

func TestCommandsAndComposableOptions(t *testing.T) {
	for _, tc := range []struct {
		args string
		want Invocation
	}{
		{"", Invocation{}},
		{"--offline --no-splash", Invocation{Offline: true, NoSplash: true}},
		{"--no-splash -o", Invocation{Offline: true, NoSplash: true}},
		{"demo --no-splash", Invocation{Command: Demo, NoSplash: true}},
		{"--no-splash demo", Invocation{Command: Demo, NoSplash: true}},
		{"-d --no-splash", Invocation{Command: Demo, NoSplash: true}},
		{"version", Invocation{Command: Version}},
		{"-v", Invocation{Command: Version}},
		{"update", Invocation{Command: Update}},
		{"sync --account me@example.com", Invocation{Command: Sync, Account: "me@example.com"}},
		{"--account me@example.com sync", Invocation{Command: Sync, Account: "me@example.com"}},
		{"-a=me@example.com check", Invocation{Command: Check, Account: "me@example.com"}},
		{"passwords -a me@example.com", Invocation{Command: Passwords, Account: "me@example.com"}},
		{"check --account=me@example.com", Invocation{Command: Check, Account: "me@example.com"}},
		{"--account=me@example.com check -a ME@example.com", Invocation{Command: Check, Account: "ME@example.com"}},
		{"--offline=false --no-splash=true", Invocation{NoSplash: true}},
		{"--demo=false --offline", Invocation{Offline: true}},
		{"-- demo", Invocation{Command: Demo}},
	} {
		t.Run(tc.args, func(t *testing.T) {
			got, err := Parse(strings.Fields(tc.args))
			if err != nil || got != tc.want {
				t.Fatalf("Parse(%q) = %+v, %v; want %+v", tc.args, got, err, tc.want)
			}
		})
	}
}

func TestLegacyActionAliases(t *testing.T) {
	for _, tc := range []struct {
		name string
		want Command
	}{
		{"demo", Demo}, {"version", Version}, {"sync-once", Sync},
		{"check-connection", Check}, {"password-setup", Passwords},
	} {
		for _, prefix := range []string{"--", "-", ""} {
			t.Run(prefix+tc.name, func(t *testing.T) {
				got, err := Parse([]string{prefix + tc.name})
				if err != nil || got.Command != tc.want {
					t.Fatalf("alias failed: %+v, %v", got, err)
				}
			})
		}
	}
}

func TestRejectsConflictingActionsAndIrrelevantOptions(t *testing.T) {
	for _, tc := range []struct {
		args, want string
	}{
		{"demo version", "choose one command"},
		{"-d -v", "choose one command"},
		{"--sync-once --check-connection", "choose one command"},
		{"sync --offline", "--offline"},
		{"--offline sync", "--offline"},
		{"demo --offline", "--offline"},
		{"check --no-splash", "--no-splash"},
		{"update --account me@example.com", "--account"},
		{"--account me@example.com", "--account"},
		{"demo --account me@example.com", "--account"},
		{"version --no-splash", "--no-splash"},
		{"sync -a one@example.com -a two@example.com", "choose one --account"},
		{"sync --account", "needs an email"},
		{"sync --account --help", "needs an email"},
		{"sync --account=", "complete email"},
		{"sync -a not-an-email", "complete email"},
		{"unknown", "unknown command"},
		{"--unknown", "unknown option"},
		{"--offline=maybe", "true or false"},
		{"-- -v", "unknown command"},
		{"demo -- --no-splash", "unknown command"},
	} {
		t.Run(tc.args, func(t *testing.T) {
			_, err := Parse(strings.Fields(tc.args))
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error = %v; want %q", err, tc.want)
			}
		})
	}
}

func TestHelpWorksForEveryCommandWithoutRunningIt(t *testing.T) {
	for _, name := range []string{"", "demo", "version", "update", "sync", "check", "passwords"} {
		for _, args := range [][]string{
			strings.Fields("help " + name),
			strings.Fields(name + " --help"),
			strings.Fields("-h " + name),
		} {
			got, err := Parse(args)
			if err != nil || !got.Help || string(got.Command) != name {
				t.Fatalf("help parse failed for %v: %+v, %v", args, got, err)
			}
			var out bytes.Buffer
			Help(&out, got.Command)
			if !strings.Contains(out.String(), "Usage: hane") {
				t.Fatal("missing usage")
			}
			if name == "update" && strings.Contains(out.String(), "--account") {
				t.Fatal("help offered an unrelated option")
			}
		}
	}
}
