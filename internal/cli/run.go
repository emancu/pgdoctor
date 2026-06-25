package cli

import (
	"fmt"
	"net/url"
	"os"
	"sort"

	"github.com/fatih/color"
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

			w := cmd.OutOrStdout()

			var renderer pgdoctor.Renderer
			if opts.output == "json" {
				renderer = pgdoctor.NewJSONRenderer(w)
			} else {
				renderer = pgdoctor.NewTextRenderer(w, textOptions(opts, dsn))
			}

			maxSeverity := check.SeverityOK
			runOpts.OnReport = func(r *check.Report) {
				if r.Severity > maxSeverity {
					maxSeverity = r.Severity
				}
				renderer.Report(r)
			}
			pgdoctor.Run(ctx, conn, runOpts)

			if err := renderer.Flush(); err != nil {
				fmt.Fprintf(os.Stderr, "Error: %v\n", err)
				return &SilentError{ExitCode: 1}
			}

			return runExitError(opts.output, maxSeverity)
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

// runExitError maps the run outcome to a process exit code. Text output exits
// non-zero when any check fails; JSON output always exits zero after a successful
// encode — JSON consumers read pass/fail from the document, and automation treats
// process success as "valid JSON was produced". This preserves the pre-renderer
// CLI behavior, where the two output paths had different exit semantics.
func runExitError(output string, maxSeverity check.Severity) error {
	if output != "json" && maxSeverity == check.SeverityFail {
		return &SilentError{ExitCode: 1}
	}
	return nil
}

// textOptions maps the CLI's runOptions onto the library's TextOptions.
func textOptions(opts *runOptions, dsn string) pgdoctor.TextOptions {
	return pgdoctor.TextOptions{
		Title:       fmt.Sprintf("Database Health Check: %s", parseDSNLabel(dsn)),
		Detail:      pgdoctor.Detail(opts.detail),
		HidePassing: opts.hidePassing,
		NoColor:     color.NoColor,
		FooterHints: []string{
			"To see more: pgdoctor run ... --detail verbose",
			"To see how to fix: pgdoctor explain <check-id>",
		},
	}
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
