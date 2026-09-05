package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/alessandrocorsico/oskar/internal/checks"
	"github.com/alessandrocorsico/oskar/internal/cluster"
	"github.com/alessandrocorsico/oskar/internal/report"
)

// ErrFindings is returned by scan when findings reach the --fail-on
// severity. The report has already been written; main maps it to exit 2.
var ErrFindings = errors.New("findings at or above the --fail-on severity")

// Exit codes of the oskar binary.
const (
	ExitClean    = 0
	ExitError    = 1
	ExitFindings = 2
)

// ExitCode maps the error returned by Execute to a process exit code.
func ExitCode(err error) int {
	switch {
	case err == nil:
		return ExitClean
	case errors.Is(err, ErrFindings):
		return ExitFindings
	default:
		return ExitError
	}
}

type scanOptions struct {
	kubeconfig        string
	kubecontext       string
	namespace         string
	output            string
	only              []string
	skip              []string
	excludeNamespaces []string
	certWarnDays      int
	stuckThreshold    time.Duration
	requestTimeout    time.Duration
	noColor           bool
	strict            bool
	failOn            string

	// Injection points for tests.
	out     io.Writer
	errOut  io.Writer
	connect func(*scanOptions) (*cluster.Connection, cluster.Clients, error)
}

func defaultConnect(o *scanOptions) (*cluster.Connection, cluster.Clients, error) {
	conn, err := cluster.BuildConfig(cluster.ConfigOptions{
		Kubeconfig: o.kubeconfig,
		Context:    o.kubecontext,
		Timeout:    o.requestTimeout,
		UserAgent:  "oskar/" + version,
	})
	if err != nil {
		return nil, cluster.Clients{}, fmt.Errorf("building kubernetes client config: %w", err)
	}
	clients, err := cluster.NewClients(conn.Config)
	if err != nil {
		return nil, cluster.Clients{}, err
	}
	return conn, clients, nil
}

func newScanCmd() *cobra.Command {
	o := &scanOptions{out: os.Stdout, errOut: os.Stderr, connect: defaultConnect}
	cmd := &cobra.Command{
		Use:   "scan",
		Short: "Scan the cluster for silent state anomalies",
		Long: `Scan performs a single read-only pass over the cluster:

  1. it fetches a snapshot of the resources the enabled checks need,
  2. it runs every enabled check against that snapshot,
  3. it prints the findings (human-readable or JSON).

A check whose resources could not be listed (RBAC, API group not served)
is skipped with a notice rather than run on empty data. Any other failure
to reach the API server aborts the scan: a report produced without data
would be a false "all clear".

Exit codes: 0 = clean (below --fail-on threshold), 1 = runtime error,
2 = findings at or above the --fail-on severity. This makes oskar easy
to wire into CI/CD gates and pre/post-upgrade pipelines.`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runScan(cmd.Context(), o)
		},
	}
	f := cmd.Flags()
	f.StringVar(&o.kubeconfig, "kubeconfig", "", "path to kubeconfig (defaults to $KUBECONFIG or ~/.kube/config; falls back to in-cluster)")
	f.StringVar(&o.kubecontext, "context", "", "kubeconfig context to use")
	f.StringVarP(&o.namespace, "namespace", "n", "", "restrict namespaced checks to one namespace (default: all namespaces); cluster-scoped checks (webhooks, PVs, namespaces) always look at the whole cluster")
	f.StringVarP(&o.output, "output", "o", "table", "output format: table|json")
	f.StringSliceVar(&o.only, "checks", nil, "run only these checks (comma-separated, see 'oskar checks')")
	f.StringSliceVar(&o.skip, "skip-checks", nil, "skip these checks (comma-separated)")
	f.StringSliceVar(&o.excludeNamespaces, "exclude-namespaces", nil, "drop findings from these namespaces (comma-separated)")
	f.IntVar(&o.certWarnDays, "cert-warn-days", 30, "warn when TLS certificates expire within this many days")
	f.DurationVar(&o.stuckThreshold, "stuck-threshold", time.Hour, "how long a resource may be Terminating before it is flagged")
	f.DurationVar(&o.requestTimeout, "request-timeout", 2*time.Minute, "timeout for each API request (0 = none)")
	f.BoolVar(&o.strict, "strict", false, "fail (exit 1) when any resource cannot be listed, instead of skipping the checks that need it")
	f.BoolVar(&o.noColor, "no-color", false, "disable colored output")
	f.StringVar(&o.failOn, "fail-on", "warning", "exit with code 2 when findings reach this severity: critical|warning|info|never")
	return cmd
}

func runScan(ctx context.Context, o *scanOptions) error {
	threshold, err := failOnRank(o.failOn)
	if err != nil {
		return err
	}
	if o.output != "table" && o.output != "json" {
		return fmt.Errorf("unknown output format %q (expected table or json)", o.output)
	}
	selected, err := checks.Select(o.only, o.skip)
	if err != nil {
		return err
	}
	if len(selected) == 0 {
		return errors.New("no checks selected")
	}

	conn, clients, err := o.connect(o)
	if err != nil {
		return err
	}

	start := time.Now()
	fmt.Fprintf(o.errOut, "oskar %s — scanning%s...\n", version, nsSuffix(o.namespace))
	snap, err := cluster.BuildSnapshot(ctx, clients, cluster.Request{
		Namespace: o.namespace,
		Needs:     checks.Needed(selected),
		Strict:    o.strict,
	})
	if err != nil {
		return fmt.Errorf("building cluster snapshot: %w", err)
	}

	findings, statuses := checks.RunAll(ctx, selected, snap, checks.Options{
		CertWarnDays:   o.certWarnDays,
		StuckThreshold: o.stuckThreshold,
	})
	findings = dropNamespaces(findings, o.excludeNamespaces)

	notices := append([]string{}, snap.Warnings...)
	for _, st := range statuses {
		switch st.State {
		case checks.StateSkipped:
			notices = append(notices, fmt.Sprintf("check %s skipped: %s", st.Name, st.Reason))
		case checks.StateFailed:
			notices = append(notices, fmt.Sprintf("check %s failed: %s", st.Name, st.Reason))
		}
	}

	out := report.New(findings, notices, report.Scan{
		OskarVersion: version,
		Context:      conn.Context,
		Server:       conn.Server,
		Namespace:    o.namespace,
		Checks:       statuses,
	}, time.Since(start))

	switch o.output {
	case "json":
		if err := report.WriteJSON(o.out, out); err != nil {
			return err
		}
	default:
		useColor := !o.noColor && os.Getenv("NO_COLOR") == "" && isTerminal(o.out)
		report.WriteTable(o.out, out, useColor)
	}

	if threshold > 0 {
		for _, f := range findings {
			if f.Severity.Rank() >= threshold {
				return ErrFindings
			}
		}
	}
	return nil
}

func nsSuffix(ns string) string {
	if ns == "" {
		return " all namespaces"
	}
	return fmt.Sprintf(" namespace %q", ns)
}

func failOnRank(s string) (int, error) {
	switch strings.ToLower(s) {
	case "never":
		return 0, nil
	case "info":
		return 1, nil
	case "warning":
		return 2, nil
	case "critical":
		return 3, nil
	}
	return 0, fmt.Errorf("invalid --fail-on value %q (expected critical|warning|info|never)", s)
}

func dropNamespaces(fs []checks.Finding, excluded []string) []checks.Finding {
	if len(excluded) == 0 {
		return fs
	}
	skip := make(map[string]bool, len(excluded))
	for _, ns := range excluded {
		skip[strings.TrimSpace(ns)] = true
	}
	out := fs[:0]
	for _, f := range fs {
		if f.Namespace == "" || !skip[f.Namespace] {
			out = append(out, f)
		}
	}
	return out
}

func isTerminal(w io.Writer) bool {
	f, ok := w.(*os.File)
	if !ok {
		return false
	}
	fi, err := f.Stat()
	if err != nil {
		return false
	}
	return fi.Mode()&os.ModeCharDevice != 0
}
