package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"

	"github.com/dmillzilla/hane/internal/app"
	"github.com/dmillzilla/hane/internal/buildinfo"
	"github.com/dmillzilla/hane/internal/update"
)

func main() {
	if len(os.Args) > 1 && os.Args[1] == "update" {
		if len(os.Args) == 3 && (os.Args[2] == "--help" || os.Args[2] == "-h") {
			fmt.Println("Usage: hane update\n\nDownload and install the latest stable Hane release for this computer.")
			return
		}
		if len(os.Args) != 2 {
			fmt.Fprintln(os.Stderr, "Usage: hane update")
			os.Exit(2)
		}
		ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
		defer cancel()
		if err := update.Run(ctx, buildinfo.Version, os.Stdout); err != nil {
			fmt.Fprintln(os.Stderr, "Update failed:", err)
			os.Exit(1)
		}
		return
	}
	app.Run()
}
