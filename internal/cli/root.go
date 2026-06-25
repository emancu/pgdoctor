// Package cli implements the pgdoctor command-line interface.
package cli

import (
	"github.com/fatih/color"
	"github.com/spf13/cobra"
)

func Execute(version string) error {
	cmd := &cobra.Command{
		Use:   "pgdoctor",
		Short: "Checks for best-practice PostgreSQL databases",
		Long: `pgdoctor implements a suite of checks that run against PostgreSQL
databases, highlighting any potential issues or action items that the database
owner may want to address in order to ensure the health & scalability of their
database.

The list of checks is not exhaustive, but is a good baseline standard that
all production databases should pass.`,
		Version:           version,
		SilenceUsage:      true,
		SilenceErrors:     true,
		DisableAutoGenTag: true,
		CompletionOptions: cobra.CompletionOptions{
			HiddenDefaultCmd: true,
		},
	}

	var noColor bool
	cmd.PersistentFlags().BoolVar(&noColor, "no-color", false, "Disable colored output")
	cmd.PersistentFlags().BoolVar(&noColor, "no-colour", false, "Disable colored output")
	_ = cmd.PersistentFlags().MarkHidden("no-colour")

	cmd.PersistentPreRun = func(_ *cobra.Command, _ []string) {
		if noColor {
			color.NoColor = true
		}
	}

	cmd.AddCommand(newRunCommand())
	cmd.AddCommand(newListCommand())
	cmd.AddCommand(newExplainCommand())

	cmd.SetHelpCommand(&cobra.Command{Hidden: true})

	return cmd.Execute()
}

// SilentError is an error that has already been reported to the user.
// The main function should exit with the given exit code without printing anything.
type SilentError struct {
	ExitCode int
}

func (e *SilentError) Error() string {
	return ""
}
