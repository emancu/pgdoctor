package cli

import (
	"fmt"
	"sort"

	"github.com/fatih/color"
	"github.com/spf13/cobra"

	"github.com/emancu/pgdoctor"
	"github.com/emancu/pgdoctor/check"
)

func newListCommand() *cobra.Command {
	var categories []string

	cmd := &cobra.Command{
		Use:   "list",
		Short: "List all available checks",
		Long:  `List all available pgdoctor checks organized by category.`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			checks := pgdoctor.AllChecks()

			grouped := map[string][]check.Metadata{}
			for _, pkg := range checks {
				m := pkg.Metadata()
				grouped[string(m.Category)] = append(grouped[string(m.Category)], m)
			}

			if len(categories) > 0 {
				filtered := map[string][]check.Metadata{}
				for _, cat := range categories {
					if checks, exists := grouped[cat]; exists {
						filtered[cat] = checks
					} else {
						fmt.Fprintf(cmd.OutOrStderr(), "Warning: unknown category '%s' - ignoring\n", cat)
					}
				}
				if len(filtered) > 0 {
					grouped = filtered
				}
			}

			categoryNames := make([]string, 0, len(grouped))
			for cat := range grouped {
				categoryNames = append(categoryNames, cat)
			}
			sort.Strings(categoryNames)

			w := cmd.OutOrStdout()
			fmt.Fprintln(w, "Available Checks:")
			fmt.Fprintln(w, "─────────────────")
			fmt.Fprintln(w)

			for _, cat := range categoryNames {
				categoryColor := color.New(color.FgCyan, color.Bold)
				fmt.Fprintf(w, "%s:\n", categoryColor.Sprint(cat))

				for _, c := range grouped[cat] {
					fmt.Fprintf(w, "  • %s (%s/%s)\n",
						color.New(color.Bold).Sprint(c.Name),
						c.Category,
						c.CheckID)
					fmt.Fprintf(w, "    %s\n", c.Description)
					fmt.Fprintln(w)
				}
			}

			fmt.Fprintf(w, "Total: %d checks\n", len(checks))
			fmt.Fprintln(w)
			fmt.Fprintln(w, "Use 'pgdoctor explain <check-id>' for detailed information")
			fmt.Fprintln(w)

			return nil
		},
	}

	cmd.Flags().StringSliceVar(&categories, "category", nil, "Filter by category")

	return cmd
}
