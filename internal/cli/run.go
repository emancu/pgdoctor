package cli

import (
	"fmt"
	"net/url"
	"os"

	"github.com/jackc/pgx/v5"
	"github.com/spf13/cobra"

	"github.com/emancu/pgdoctor"
	"github.com/emancu/pgdoctor/check"
)

type detailLevel string

const (
	detailSummary detailLevel = "summary"
	detailBrief   detailLevel = "brief"
	detailVerbose detailLevel = "verbose"
	detailDebug   detailLevel = "debug"
)

type runOptions struct {
	ignored     []string
	only        []string
	preset      string
	detail      string
	hidePassing bool
	output      string
}

func newRunCommand() *cobra.Command {
	opts := &runOptions{}

	cmd := &cobra.Command{
		Use:   "run <DSN>",
		Short: "Run health checks against a PostgreSQL database",
		Long: `Run a suite of health checks against a PostgreSQL database to identify
potential issues, misconfigurations, or areas for optimization.

By default, all checks are shown in summary mode. Use --detail to control
the level of detail, and --hide-passing to only show failures and warnings.`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			// Resolve DSN: positional argument > environment variable
			var dsn string
			if len(args) > 0 {
				dsn = args[0]
			} else {
				dsn = os.Getenv("PGDOCTOR_DSN")
			}
			if dsn == "" {
				return fmt.Errorf("connection string required: pgdoctor run <DSN> or set PGDOCTOR_DSN environment variable")
			}

			// Default to 'brief' detail when --only is used
			if len(opts.only) > 0 && !cmd.Flags().Changed("detail") {
				opts.detail = string(detailBrief)
			}

			ctx := cmd.Context()

			conn, err := pgx.Connect(ctx, dsn)
			if err != nil {
				fmt.Fprintf(os.Stderr, "Error: failed to connect to database: %v\n", err)
				return &SilentError{ExitCode: 2}
			}
			defer conn.Close(ctx)

			allChecks := pgdoctor.AllChecks()

			// Apply preset filter
			if opts.preset != presetAll {
				presetChecks := getPresetChecks(opts.preset)
				if len(opts.only) == 0 {
					opts.only = presetChecks
				} else {
					opts.only = intersect(opts.only, presetChecks)
				}
			}

			// Validate filters
			validOnly, invalidOnly := pgdoctor.ValidateFilters(allChecks, opts.only)
			validIgnored, invalidIgnored := pgdoctor.ValidateFilters(allChecks, opts.ignored)

			var allInvalid []string
			allInvalid = append(allInvalid, invalidOnly...)
			allInvalid = append(allInvalid, invalidIgnored...)

			if len(allInvalid) > 0 {
				fmt.Fprintf(os.Stderr, "Warning: ignoring invalid filter(s): %v\n\n", allInvalid)
			}

			if len(opts.only) > 0 && len(validOnly) == 0 {
				fmt.Fprintf(os.Stderr, "Error: no valid checks found for --only filter(s): %v\n", invalidOnly)
				return &SilentError{ExitCode: 1}
			}

			reports, err := pgdoctor.Run(ctx, conn, allChecks, nil, validOnly, validIgnored)
			if err != nil {
				fmt.Fprintf(os.Stderr, "Error: %v\n", err)
				return &SilentError{ExitCode: 1}
			}

			// Format and print output
			w := cmd.OutOrStdout()

			if opts.output == "json" {
				if err := formatJSON(w, reports); err != nil {
					fmt.Fprintf(os.Stderr, "Error: %v\n", err)
					return &SilentError{ExitCode: 1}
				}
				return nil
			}

			dbLabel := parseDSNLabel(dsn)
			maxSeverity := formatReport(w, reports, opts, dbLabel)

			if maxSeverity > check.SeverityWarn {
				return &SilentError{ExitCode: 1}
			}

			return nil
		},
	}

	cmd.Flags().StringSliceVar(&opts.ignored, "ignore", nil, "Checks or categories to ignore")
	cmd.Flags().StringSliceVar(&opts.only, "only", nil, "Only run these checks or categories")
	cmd.Flags().StringVar(&opts.preset, "preset", presetAll, "Check preset: all (default), triage")
	cmd.Flags().StringVar(&opts.detail, "detail", string(detailSummary), "Detail level: summary, brief, verbose, debug")
	cmd.Flags().BoolVar(&opts.hidePassing, "hide-passing", false, "Hide passing checks")
	cmd.Flags().StringVar(&opts.output, "output", "text", "Output format: text (default), json")

	return cmd
}

// parseDSNLabel extracts a human-readable label from a DSN.
func parseDSNLabel(dsn string) string {
	u, err := url.Parse(dsn)
	if err != nil {
		return dsn
	}

	host := u.Hostname()
	if host == "" {
		return dsn
	}

	db := ""
	if u.Path != "" && u.Path != "/" {
		db = u.Path[1:] // strip leading /
	}

	if db != "" {
		return fmt.Sprintf("%s/%s", host, db)
	}
	return host
}
