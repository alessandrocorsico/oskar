// Package cli implements the oskar commands: scan, checks and version.
package cli

import (
	"context"

	"github.com/spf13/cobra"
)

// version is injected at build time via -ldflags (see Makefile, Dockerfile
// and .goreleaser.yaml).
var version = "dev"

// Execute runs the oskar CLI. The context carries signal-driven cancellation
// so a Ctrl-C or a Job deadline interrupts in-flight API calls.
func Execute(ctx context.Context) error {
	return newRootCmd().ExecuteContext(ctx)
}

func newRootCmd() *cobra.Command {
	root := &cobra.Command{
		Use:   "oskar",
		Short: "OSKAR — Open Source Kubernetes Anomaly Radar",
		Long: `OSKAR — Open Source Kubernetes Anomaly Radar.

OSKAR performs a read-only scan of your cluster and reports silent state
inconsistencies: the broken links between objects that raise no events but
cause the weirdest production incidents — ghost endpoints, dead admission
webhooks, ingresses pointing at nothing, certificates about to expire,
resources stuck in Terminating, and friends.

It only needs list permissions and works on any conformant cluster
(EKS, AKS, GKE, OpenShift, RKE2, kind, ...).`,
		Version:       version,
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	root.AddCommand(newScanCmd(), newChecksCmd(), newVersionCmd())
	return root
}
