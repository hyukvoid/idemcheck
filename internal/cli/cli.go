// Package cli wires the idemcheck command line.
package cli

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"github.com/hyukvoid/idemcheck/internal/buildinfo"
	"github.com/hyukvoid/idemcheck/internal/config"
	"github.com/hyukvoid/idemcheck/internal/engine"
	"github.com/hyukvoid/idemcheck/internal/fingerprint"
	"github.com/hyukvoid/idemcheck/internal/httpx"
	"github.com/hyukvoid/idemcheck/internal/models"
	"github.com/hyukvoid/idemcheck/internal/report"
)

const ruleWidth = 34

// exitError carries a deliberate exit code through cobra's error path.
type exitError struct {
	code int
	err  error
}

func (e *exitError) Error() string {
	if e.err != nil {
		return e.err.Error()
	}
	return fmt.Sprintf("exit %d", e.code)
}

// fail marks a configuration/execution failure (exit code 2).
func fail(err error) error { return &exitError{code: models.ExitFailure, err: err} }

// Execute runs the CLI and returns the process exit code:
// 0 pass, 1 idempotency violation, 2 configuration/execution failure.
func Execute() int {
	var exitCode = models.ExitPass

	root := &cobra.Command{
		Use:           "idemcheck",
		Short:         "Break your idempotency implementation before production does.",
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	root.AddCommand(newTestCmd(&exitCode))
	root.AddCommand(&cobra.Command{
		Use:   "version",
		Short: "Print idemcheck version",
		Run: func(cmd *cobra.Command, args []string) {
			fmt.Fprintf(cmd.OutOrStdout(), "%s %s\n", models.ToolName, buildinfo.Display())
		},
	})

	if err := root.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, "Error:", err)
		if ee, ok := err.(*exitError); ok {
			return ee.code
		}
		return models.ExitFailure
	}
	return exitCode
}

// stringList collects repeatable flags (-H ... -H ...).
type stringList []string

func (s *stringList) String() string     { return strings.Join(*s, ", ") }
func (s *stringList) Set(v string) error { *s = append(*s, v); return nil }
func (s *stringList) Type() string       { return "stringList" }

func newTestCmd(exitCode *int) *cobra.Command {
	var (
		url         string
		method      string
		body        string
		bodyFile    string
		headers     stringList
		keyHeader   string
		key         string
		concurrency int
		repeat      int
		format      string
		allowRemote bool
		timeout     time.Duration
		verbose     bool
		configPath  string
		ignoreJSON  []string
		ignoreHdr   []string
		maxConc     int
		maxRepeat   int
	)

	cmd := &cobra.Command{
		Use:   "test",
		Short: "Test an endpoint for idempotency violations",
		Long: `IdemCheck sends sequential and concurrent duplicate requests sharing one
Idempotency-Key and reports whether the endpoint produces more than one
semantic response — the signature of an idempotency race condition.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			opts := &config.Options{
				URL:            url,
				Method:         method,
				Headers:        headers,
				KeyHeader:      keyHeader,
				Key:            key,
				Concurrency:    concurrency,
				Repeat:         repeat,
				Format:         format,
				AllowRemote:    allowRemote,
				Timeout:        timeout,
				Verbose:        verbose,
				IgnoreJSON:     ignoreJSON,
				IgnoreHeader:   ignoreHdr,
				MaxConcurrency: maxConc,
				MaxRepeat:      maxRepeat,
			}

			// YAML config provides defaults; flags append on top so explicit
			// CLI input always wins by inclusion.
			if configPath != "" {
				fj, fh, err := config.LoadConfigFile(configPath)
				if err != nil {
					return fail(err)
				}
				opts.IgnoreJSON = append(fj, opts.IgnoreJSON...)
				opts.IgnoreHeader = append(fh, opts.IgnoreHeader...)
			}

			switch {
			case body != "" && bodyFile != "":
				return fail(fmt.Errorf("use only one of --body or --body-file"))
			case bodyFile != "":
				raw, err := os.ReadFile(bodyFile)
				if err != nil {
					return fail(fmt.Errorf("read --body-file: %w", err))
				}
				opts.Body = raw
				opts.BodySource = bodyFile
				opts.BodyIsFile = true
			case body != "":
				opts.Body = []byte(body)
				opts.BodySource = body
			}

			if err := opts.Validate(); err != nil {
				return fail(err)
			}
			warn, err := opts.SafetyGuard()
			if err != nil {
				return fail(err)
			}
			var warnings []string
			if warn != "" {
				warnings = append(warnings, warn)
			}
			if opts.Key == "" {
				opts.Key = config.NewKey()
			}
			return runTest(cmd, opts, warnings, exitCode)
		},
	}

	f := cmd.Flags()
	f.StringVar(&url, "url", "", "target URL (required)")
	f.StringVar(&method, "method", config.DefaultMethod, "HTTP method")
	f.StringVar(&body, "body", "", "request body (inline)")
	f.StringVar(&bodyFile, "body-file", "", "request body from file")
	f.VarP(&headers, "header", "H", "extra header \"Name: value\" (repeatable)")
	f.StringVar(&keyHeader, "key-header", config.DefaultKeyHeader, "idempotency key header name")
	f.StringVar(&key, "key", "", "idempotency key (generated when omitted; set for deterministic repro)")
	f.IntVar(&concurrency, "concurrency", config.DefaultConcurrency, "concurrent request count")
	f.IntVar(&repeat, "repeat", config.DefaultRepeat, "sequential repeat count")
	f.StringVar(&format, "format", config.DefaultFormat, "output format: text or json")
	f.BoolVar(&allowRemote, "allow-remote", false, "permit testing a non-local host")
	f.DurationVar(&timeout, "timeout", config.DefaultTimeout, "per-request HTTP timeout")
	f.BoolVarP(&verbose, "verbose", "v", false, "show per-request timing detail")
	f.StringVar(&configPath, "config", "", "YAML config file (response ignore lists)")
	f.StringArrayVar(&ignoreJSON, "ignore-json", nil, "JSON path to ignore, repeatable (e.g. $.request_id)")
	f.StringArrayVar(&ignoreHdr, "ignore-header", nil, "header name to ignore, repeatable (e.g. x-custom-trace)")
	f.IntVar(&maxConc, "max-concurrency", config.DefaultMaxConcurrency, "safety ceiling for --concurrency")
	f.IntVar(&maxRepeat, "max-repeat", config.DefaultRepeat*10, "safety ceiling for --repeat")

	return cmd
}

// runTest executes the full check matrix and renders the chosen format.
func runTest(cmd *cobra.Command, opts *config.Options, warnings []string, exitCode *int) error {
	out := cmd.OutOrStdout()

	spec := httpx.Spec{
		Method:    opts.Method,
		URL:       opts.URL,
		Body:      opts.Body,
		KeyHeader: opts.KeyHeader,
		Header:    http.Header{},
	}
	for _, h := range opts.Headers {
		name, value, err := config.SplitHeader(h)
		if err != nil {
			return fail(err)
		}
		spec.Header.Add(name, value)
	}

	// Ctrl-C cancels the barrier and in-flight requests cleanly.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	client := httpx.NewClient(opts.Timeout)
	runner := &engine.Runner{
		Client:      client,
		Spec:        spec,
		FPOpts:      fingerprint.Options{IgnoreJSONPaths: opts.IgnoreJSON, IgnoreHeaders: opts.IgnoreHeader},
		BaseKey:     opts.Key,
		Repeat:      opts.Repeat,
		Concurrency: opts.Concurrency,
	}

	results, err := runner.Run(ctx)
	if err != nil {
		return fail(fmt.Errorf("execution aborted: %w", err))
	}

	res := report.Assemble(report.AssembleInput{
		Target: models.Target{
			URL:       opts.URL,
			Method:    opts.Method,
			KeyHeader: opts.KeyHeader,
			Key:       opts.Key,
		},
		Results:  results,
		Options:  *opts,
		Warnings: warnings,
	})

	*exitCode = res.Summary.ExitCode

	switch opts.Format {
	case "json":
		if err := report.JSON(out, res); err != nil {
			return fail(err)
		}
	default:
		report.Terminal(out, res)
		if opts.Verbose {
			renderTimings(out, res)
		}
	}
	return nil
}

// renderTimings prints per-request timing so users can verify the burst was
// genuinely simultaneous (printed before the main report in verbose mode).
func renderTimings(out interface{ Write([]byte) (int, error) }, res *models.Result) {
	for _, c := range res.Checks {
		if len(c.Timings) == 0 {
			continue
		}
		fmt.Fprintf(out, "\nTimings — %s (ms after barrier release | latency):\n", c.Name)
		var maxOffset int64
		for _, t := range c.Timings {
			if t.StartOffset > maxOffset {
				maxOffset = t.StartOffset
			}
			fmt.Fprintf(out, "  worker %2d: +%3d | %3d\n", t.Worker, t.StartOffset, t.Latency)
		}
		fmt.Fprintf(out, "  spread: all requests initiated within %d ms of barrier release\n", maxOffset)
	}
	fmt.Fprintln(out, "")
}
