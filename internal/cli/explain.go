package cli

import (
	"fmt"

	"github.com/charmbracelet/glamour"
	"github.com/fatih/color"
	"github.com/spf13/cobra"

	"github.com/emancu/pgdoctor"
	"github.com/emancu/pgdoctor/check"
)

func newExplainCommand() *cobra.Command {
	var sqlOnly bool

	cmd := &cobra.Command{
		Use:   "explain",
		Short: "Show documentation for a specific check",
		Long: `Show information for a specific check including:
 * Description
 * What it checks
 * Why it matters
 * How to fix
 * SQL query used`,
	}

	cmd.PersistentFlags().BoolVar(&sqlOnly, "sql-only", false, "Show only the SQL query used by the check")

	for _, pkg := range pgdoctor.AllChecks() {
		metadata := pkg.Metadata()
		cmd.AddCommand(newExplainCheckCommand(metadata, &sqlOnly))
	}

	return cmd
}

func newExplainCheckCommand(metadata check.Metadata, sqlOnly *bool) *cobra.Command {
	return &cobra.Command{
		Use:   metadata.CheckID,
		Short: metadata.Description,
		Long:  metadata.Readme,
		RunE: func(cmd *cobra.Command, _ []string) error {
			var doc string
			if *sqlOnly && metadata.SQL != "" {
				doc = fmt.Sprintf("\n\n# %s (SQL)\n\n", metadata.Name)
				doc += "```sql\n"
				doc += metadata.SQL
				doc += "\n```\n"
			} else {
				doc = metadata.Readme
			}

			if color.NoColor {
				fmt.Fprintln(cmd.OutOrStdout(), doc)
			} else {
				r, err := glamour.NewTermRenderer(
					glamour.WithAutoStyle(),
					glamour.WithWordWrap(0),
				)
				if err != nil {
					return fmt.Errorf("creating markdown renderer: %w", err)
				}

				rendered, err := r.Render(doc)
				if err != nil {
					return fmt.Errorf("rendering markdown: %w", err)
				}

				fmt.Fprint(cmd.OutOrStdout(), rendered)
			}

			return nil
		},
	}
}
