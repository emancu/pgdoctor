package cli

import (
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

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
	config      string
}

func newRunCommand() *cobra.Command {
	opts := &runOptions{}

	cmd := &cobra.Command{
		Use:   "run <DSN>",
		Short: "Run health checks against a PostgreSQL database",
		Long: `Run a suite of health checks against a PostgreSQL database to identify
potential issues, misconfigurations, or areas for optimization.

By default, each check is shown in brief mode. Use --detail to control
the level of detail, and --hide-passing to hide checks and findings that passed.`,
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

			switch detailLevel(opts.detail) {
			case detailSummary, detailBrief, detailVerbose, detailDebug:
			default:
				return fmt.Errorf("unknown --detail value %q: use summary, brief, verbose, or debug", opts.detail)
			}
			if opts.output != "text" && opts.output != "json" {
				return fmt.Errorf("unknown --output value %q: use text or json", opts.output)
			}

			// Default to 'brief' detail when --only is used
			if len(opts.only) > 0 && !cmd.Flags().Changed("detail") {
				opts.detail = string(detailBrief)
			}

			var cfg check.Config
			if opts.config != "" {
				var err error
				cfg, err = loadConfig(opts.config, pgdoctor.AllChecks())
				if err != nil {
					return err
				}
			}

			allChecks := pgdoctor.AllChecks()

			opts.only = applyPreset(os.Stderr, opts.preset, opts.only)

			// Validate and apply filters
			validOnly, invalidOnly := pgdoctor.ValidateFilters(allChecks, opts.only)
			validIgnored, invalidIgnored := pgdoctor.ValidateFilters(allChecks, opts.ignored)

			var allInvalid []string
			allInvalid = append(allInvalid, invalidOnly...)
			allInvalid = append(allInvalid, invalidIgnored...)

			if len(allInvalid) > 0 {
				return fmt.Errorf("unknown check or category in --only or --ignore: %v", allInvalid)
			}

			checks := pgdoctor.Filter(allChecks, validOnly, validIgnored)
			if len(checks) == 0 {
				return fmt.Errorf("no checks selected: --ignore removes every selected check")
			}
			sortChecksByCategory(checks)

			ctx := cmd.Context()

			connConfig, err := parseDSN(dsn)
			if err != nil {
				fmt.Fprintf(os.Stderr, "Error: failed to connect to database: %v\n", err)
				return &SilentError{ExitCode: 2}
			}

			conn, err := pgx.ConnectConfig(ctx, connConfig)
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

			runOpts := pgdoctor.Options{
				Checks: checks,
				Config: cfg,
			}

			// JSON output: batch collect then render
			if opts.output == "json" {
				var reports []*check.Report
				runOpts.OnReport = pgdoctor.Collect(&reports)
				pgdoctor.Run(ctx, conn, runOpts)

				w := cmd.OutOrStdout()
				if err := formatJSON(w, reports); err != nil {
					return err
				}
				return exitStatus(reports)
			}

			// Text output: stream results with category headers
			w := cmd.OutOrStdout()
			dbLabel := dsnLabel(connConfig)
			fmt.Fprintf(w, "Database Health Check: %s\n\n", dbLabel)

			var reports []*check.Report
			var currentCategory string

			runOpts.OnReport = func(r *check.Report) {
				reports = append(reports, r)

				if hidden(r, opts) {
					return
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

			return exitStatus(reports)
		},
	}

	cmd.Flags().StringSliceVar(&opts.ignored, "ignore", nil, "Checks or categories to ignore")
	cmd.Flags().StringSliceVar(&opts.only, "only", nil, "Only run these checks or categories")
	cmd.Flags().StringVar(&opts.preset, "preset", presetAll, "Check preset: all (default), triage")
	cmd.Flags().StringVar(&opts.detail, "detail", string(detailBrief), "Detail level: summary, brief (default), verbose, debug")
	cmd.Flags().BoolVar(&opts.hidePassing, "hide-passing", false, "Hide checks and findings that passed")
	cmd.Flags().StringVar(&opts.output, "output", "text", "Output format: text (default), json")
	cmd.Flags().StringVar(&opts.config, "config", "", "YAML file with per-check settings, keyed by check ID")

	return cmd
}

func exitStatus(reports []*check.Report) error {
	for _, r := range reports {
		if r.Severity == check.SeverityFail {
			return &SilentError{ExitCode: 1}
		}
	}
	return nil
}

func sortChecksByCategory(checks []check.Package) {
	sort.SliceStable(checks, func(i, j int) bool {
		return checks[i].Metadata().Category < checks[j].Metadata().Category
	})
}

func parseDSN(dsn string) (*pgx.ConnConfig, error) {
	cfg, err := pgx.ParseConfig(dsn)
	if err != nil {
		return nil, err
	}

	if cfg.ConnectTimeout == 0 {
		cfg.ConnectTimeout = 10 * time.Second
	}
	if _, ok := cfg.RuntimeParams["application_name"]; !ok {
		cfg.RuntimeParams["application_name"] = "pgdoctor"
	}
	return cfg, nil
}

// dsnLabel extracts a human-readable label from a DSN.
func dsnLabel(cfg *pgx.ConnConfig) string {
	if cfg.Database != "" {
		return fmt.Sprintf("%s/%s", cfg.Host, cfg.Database)
	}
	return cfg.Host
}
