// Package main is the entry point for the pgdoctor CLI.
package main

import (
	"errors"
	"fmt"
	"os"
	"runtime/debug"

	"github.com/emancu/pgdoctor/internal/cli"
)

func main() {
	if err := cli.Execute(buildVersion()); err != nil {
		var silent *cli.SilentError
		if errors.As(err, &silent) {
			os.Exit(silent.ExitCode)
		}
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
}

func buildVersion() string {
	info, ok := debug.ReadBuildInfo()
	if !ok || info.Main.Version == "" || info.Main.Version == "(devel)" {
		return "dev"
	}
	return info.Main.Version
}
