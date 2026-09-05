// Command oskar is the Open Source Kubernetes Anomaly Radar: a read-only
// scanner for silent state inconsistencies in a cluster.
package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/alessandrocorsico/oskar/internal/cli"
)

func main() {
	// SIGINT/SIGTERM cancel the context, which aborts in-flight API calls
	// instead of leaving a half-finished scan hanging (a CronJob deadline
	// sends SIGTERM).
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	err := cli.Execute(ctx)
	stop()
	if err != nil && !errors.Is(err, cli.ErrFindings) {
		fmt.Fprintln(os.Stderr, "Error:", err)
	}
	os.Exit(cli.ExitCode(err))
}
