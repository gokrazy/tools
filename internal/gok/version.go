package gok

import (
	"context"
	"fmt"
	"io"

	"github.com/gokrazy/tools/internal/version"
	"github.com/spf13/cobra"
)

// versionCmd is gok version.
var versionCmd = &cobra.Command{
	Use:   "version",
	Short: "Print gok version",
	Long:  `Print gok version`,
	RunE: func(cmd *cobra.Command, args []string) error {
		return versionImpl.run(cmd.Context(), args, cmd.OutOrStdout(), cmd.OutOrStderr())
	},
}

type versionImplConfig struct{}

var versionImpl versionImplConfig

func (r *versionImplConfig) run(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	fmt.Fprintf(stdout, "%s\n", version.Read())
	return nil
}
