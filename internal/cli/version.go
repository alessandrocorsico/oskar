package cli

import (
	"fmt"
	"runtime"

	"github.com/spf13/cobra"
)

func newVersionCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print the oskar version",
		Run: func(_ *cobra.Command, _ []string) {
			fmt.Printf("%s (%s %s/%s)\n", version, runtime.Version(), runtime.GOOS, runtime.GOARCH)
		},
	}
}
