package cmd

import (
	"fmt"
	"log"

	"github.com/gokrazy/internal/instanceflag"
	"github.com/gokrazy/tools/internal/version"
	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

var RootCmd = &cobra.Command{
	Use:   "gok",
	Short: "top-level CLI entry point for all things gokrazy",
	Long: `building and deploying new gokrazy images, managing your ~/gokrazy/
directory, building and running Go programs from your local Go workspace,
etc.`,
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

func Execute() {
	if err := RootCmd.Execute(); err != nil {
		log.Fatal(err)
	}
}

func init() {
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
	// TODO: newCmd
	// TODO: editCmd
}
