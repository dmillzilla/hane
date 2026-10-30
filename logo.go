package main

import (
	_ "embed"
	"strings"
)

// The HANE block wordmark uses the same adaptive gradient as the rest of the UI.
// Embedding it makes the executable independent of the working directory.
//
//go:embed assets/logo.txt
var logoArt string

func logo() []string {
	return strings.Split(strings.TrimRight(logoArt, "\r\n"), "\n")
}
