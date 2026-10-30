package main

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"os/signal"

	"github.com/dmillzilla/hane/internal/app"
	"github.com/dmillzilla/hane/internal/buildinfo"
	"github.com/dmillzilla/hane/internal/cli"
	"github.com/dmillzilla/hane/internal/update"
)

type handlers struct {
	open                   func(app.Options) error
	sync, check, passwords func(string) error
	update                 func(context.Context, string, io.Writer) error
}

func main() {
	args := os.Args[1:]
	// Private subprocesses used by the UI never enter the public CLI parser.
	if len(args) > 0 && args[0] == "__keyring" {
		os.Exit(app.Keyring(args[1:]))
	}
	if len(args) > 0 && args[0] == "__passwords" {
		if len(args) != 2 {
			os.Exit(2)
		}
		err := app.Passwords(args[1])
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
		}
		fmt.Print("Press Enter to return…")
		_, _ = bufio.NewReader(os.Stdin).ReadString('\n')
		if err != nil {
			os.Exit(1)
		}
		return
	}
	os.Exit(run(context.Background(), args, os.Stdout, os.Stderr, handlers{
		open: app.Run, sync: app.Sync, check: app.Check, passwords: app.Passwords,
		update: func(parent context.Context, version string, out io.Writer) error {
			ctx, cancel := signal.NotifyContext(parent, os.Interrupt)
			defer cancel()
			return update.Run(ctx, version, out)
		},
	}))
}

func run(ctx context.Context, args []string, out, errout io.Writer, h handlers) int {
	invocation, err := cli.Parse(args)
	if err != nil {
		fmt.Fprintln(errout, "hane:", err)
		fmt.Fprintln(errout, "Run hane help or hane help <command> for usage.")
		return 2
	}
	if invocation.Help {
		cli.Help(out, invocation.Command)
		return 0
	}
	switch invocation.Command {
	case cli.Version:
		fmt.Fprintln(out, "Hane", buildinfo.Version)
	case cli.Update:
		err = h.update(ctx, buildinfo.Version, out)
	case cli.Sync:
		err = h.sync(invocation.Account)
	case cli.Check:
		err = h.check(invocation.Account)
	case cli.Passwords:
		err = h.passwords(invocation.Account)
	default:
		err = h.open(app.Options{Demo: invocation.Command == cli.Demo, Offline: invocation.Offline, NoSplash: invocation.NoSplash})
	}
	if err != nil {
		fmt.Fprintln(errout, "hane:", err)
		return 1
	}
	return 0
}
