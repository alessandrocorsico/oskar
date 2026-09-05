package cli

import (
	"fmt"
	"os"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/alessandrocorsico/oskar/internal/checks"
)

func newChecksCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "checks",
		Short: "List all available checks and the resources each one lists",
		RunE: func(_ *cobra.Command, _ []string) error {
			tw := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
			fmt.Fprintln(tw, "NAME\tRESOURCES\tDESCRIPTION")
			for _, c := range checks.All() {
				var res []string
				for _, n := range c.Needs() {
					r := string(n.Resource)
					if n.ClusterWide {
						r += "*"
					}
					if n.Optional {
						r += "?"
					}
					res = append(res, r)
				}
				fmt.Fprintf(tw, "%s\t%s\t%s\n", c.Name(), strings.Join(res, ","), c.Description())
			}
			if err := tw.Flush(); err != nil {
				return err
			}
			fmt.Fprintln(os.Stdout, "\n* listed cluster-wide even with --namespace   ? optional (check still runs without it)")
			return nil
		},
	}
}
