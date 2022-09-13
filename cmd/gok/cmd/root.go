package cmd

import (
	"log"

	"github.com/spf13/cobra"
)

var RootCmd = &cobra.Command{
	Use:   "gok",
	Short: "top-level CLI entry point for all things gokrazy",
	Long: `building and deploying new gokrazy images, managing your ~/gokrazy/
directory, building and running Go programs from your local Go workspace,
etc.`,
	SilenceErrors: true,
	SilenceUsage:  true,
}

func Execute() {
	if err := RootCmd.Execute(); err != nil {
		log.Fatal(err)
	}
}

func init() {
	RootCmd.AddCommand(runCmd)
	RootCmd.AddCommand(logsCmd)
}
