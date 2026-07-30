package cli

import (
	"fmt"
	"net/url"
	"os"
	"sort"
	"strings"

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

			// Set statement_timeout so PostgreSQL kills individual slow queries.
			if _, err := conn.Exec(ctx, fmt.Sprintf("SET statement_timeout = %d", pgdoctor.DefaultStatementTimeoutMs)); err != nil {
				fmt.Fprintf(os.Stderr, "Error: failed to set statement_timeout: %v\n", err)
				return &SilentError{ExitCode: 2}
			}

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

			// Validate and apply filters
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

			checks := pgdoctor.Filter(allChecks, validOnly, validIgnored)
			sortChecksByCategory(checks)

			runOpts := pgdoctor.Options{
				Checks: checks,
			}

			// JSON output: batch collect then render
			if opts.output == "json" {
				var reports []*check.Report
				runOpts.OnReport = pgdoctor.Collect(&reports)
				pgdoctor.Run(ctx, conn, runOpts)

				w := cmd.OutOrStdout()
				if err := formatJSON(w, reports); err != nil {
					fmt.Fprintf(os.Stderr, "Error: %v\n", err)
					return &SilentError{ExitCode: 1}
				}
				return nil
			}

			// Text output: stream results with category headers
			w := cmd.OutOrStdout()
			dbLabel := parseDSNLabel(dsn)
			fmt.Fprintf(w, "Database Health Check: %s\n\n", dbLabel)

			var reports []*check.Report
			var currentCategory string
			maxSeverity := check.SeverityPass

			runOpts.OnReport = func(r *check.Report) {
				reports = append(reports, r)
				if r.Severity > maxSeverity {
					maxSeverity = r.Severity
				}

				// Print category header on transition
				cat := string(r.Category)
				if cat != currentCategory {
					if currentCategory != "" {
						fmt.Fprintln(w)
					}
					title := strings.ToUpper(cat)
					fmt.Fprintln(w, title)
					fmt.Fprintln(w, strings.Repeat("─", len(title)))
					currentCategory = cat
				}

				if r.Severity == check.SeverityPass && opts.hidePassing {
					return
				}

				if opts.detail == string(detailSummary) {
					printCheckSummary(w, r, opts)
				} else {
					printCheckReport(w, r, opts)
				}
			}
			pgdoctor.Run(ctx, conn, runOpts)

			fmt.Fprintln(w)
			printSummary(w, reports)

			if opts.detail == string(detailSummary) || opts.detail == string(detailBrief) {
				dimFunc := dimColor()
				fmt.Fprintf(w, "%s\n", dimFunc("To see more: pgdoctor run ... --detail verbose"))
				fmt.Fprintf(w, "%s\n", dimFunc("To see how to fix: pgdoctor explain <check-id>"))
				fmt.Fprintln(w)
			}

			if maxSeverity == check.SeverityFail {
				return &SilentError{ExitCode: 1}
			}

			return nil
		},
	}

	cmd.Flags().StringSliceVar(&opts.ignored, "ignore", nil, "Checks or categories to ignore")
	cmd.Flags().StringSliceVar(&opts.only, "only", nil, "Only run these checks or categories")
	cmd.Flags().StringVar(&opts.preset, "preset", presetAll, "Check preset: all (default), triage")
	cmd.Flags().StringVar(&opts.detail, "detail", string(detailBrief), "Detail level: summary, brief (default), verbose, debug")
	cmd.Flags().BoolVar(&opts.hidePassing, "hide-passing", false, "Hide passing checks")
	cmd.Flags().StringVar(&opts.output, "output", "text", "Output format: text (default), json")

	return cmd
}

func sortChecksByCategory(checks []check.Package) {
	sort.SliceStable(checks, func(i, j int) bool {
		return checks[i].Metadata().Category < checks[j].Metadata().Category
	})
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
