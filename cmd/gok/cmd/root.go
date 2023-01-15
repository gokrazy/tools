package cmd

import (
	"fmt"

	"github.com/gokrazy/internal/instanceflag"
	"github.com/gokrazy/tools/internal/version"
	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

var RootCmd = &cobra.Command{
	Use:   "gok",
	Short: "top-level CLI entry point for all things gokrazy",
	Long: `The gok tool is your main entrypoint to gokrazy and allows you to:

1. Create new gokrazy instances (gok new),
2. Deploy gokrazy instances to storage devices like SD cards (gok overwrite),
3. Update gokrazy instances over the network (gok update),
4. (For development) Run individual programs on a running gokrazy instance (gok run).

If you are unfamiliar with gokrazy, please follow:
https://gokrazy.org/quickstart/
`,
	SilenceErrors: true,
	SilenceUsage:  true,
	RunE: func(cmd *cobra.Command, args []string) error {
		versionVal, err := cmd.Flags().GetBool("version")
		if err != nil {
			return fmt.Errorf("BUG: version flag declared as non-bool")
		}
		if versionVal {
			fmt.Println(version.Read())
			return nil
		}
		return pflag.ErrHelp
	},
}

func init() {
	RootCmd.AddGroup(&cobra.Group{
		ID:    "edit",
		Title: "Commands to create and edit a gokrazy instance:",
	})
	RootCmd.AddGroup(&cobra.Group{
		ID:    "deploy",
		Title: "Commands to deploy and update a gokrazy instance:",
	})
	RootCmd.AddGroup(&cobra.Group{
		ID:    "runtime",
		Title: "Commands to work with a running gokrazy instance:",
	})
	RootCmd.Flags().Bool("version", false, "print gok version")
	// Only defined so that it appears in documentation like --help.
	//
	// Cobra only parses local flags on the target command, but they can appear
	// at any place in the command line (before or after the verb).
	instanceflag.RegisterPflags(RootCmd.Flags())
	RootCmd.AddCommand(runCmd)
	RootCmd.AddCommand(logsCmd)
	RootCmd.AddCommand(updateCmd)
	RootCmd.AddCommand(overwriteCmd)
	RootCmd.AddCommand(versionCmd)
	RootCmd.AddCommand(newCmd)
	RootCmd.AddCommand(editCmd)
	RootCmd.AddCommand(addCmd)
	RootCmd.AddCommand(getCmd)
}
