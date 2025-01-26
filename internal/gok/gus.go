package gok

import (
	"github.com/gokrazy/internal/instanceflag"
	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

// gusCmd is gok push.
var gusCmd = &cobra.Command{
	GroupID: "server",
	Use:     "gus",
	Short:   "Interacts with a remote GUS server",
	Long: `gok gus interacts with a remote GUS server.

When the --json flag is specified, the server response is printed to stdout.

Examples:
  # interact with a remote GUS server
  % gok gus diff ...
  % gok gus set ...
  % gok gus unset ...

`,
	RunE: func(cmd *cobra.Command, args []string) error {
		return pflag.ErrHelp
	},
}

func init() {
	instanceflag.RegisterPflags(gusCmd.Flags())
	gusCmd.AddCommand(diffCmd)
	gusCmd.AddCommand(setCmd)
	gusCmd.AddCommand(unsetCmd)
}
