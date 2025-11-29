package gok

import (
	"github.com/spf13/cobra"
)

// vmCmd is the gok vm subcommand, which (only) has nested commands like run.
func vmCmd() *cobra.Command {
	cmd := &cobra.Command{
		GroupID: "vm",
		Use:     "vm",
		Short:   "virtual machine",
		RunE: func(cmd *cobra.Command, args []string) error {
			return cmd.Usage()
		},
	}
	cmd.AddCommand(vmRunCmd())
	return cmd
}
