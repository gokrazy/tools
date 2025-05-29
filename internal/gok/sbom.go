package gok

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log"
	"os"

	"github.com/gokrazy/internal/config"
	"github.com/gokrazy/internal/instanceflag"
	"github.com/gokrazy/internal/updateflag"
	"github.com/gokrazy/tools/internal/packer"
	"github.com/spf13/cobra"
)

// sbomCmd is gok sbom.
var sbomCmd = &cobra.Command{
	GroupID: "deploy",
	Use:     "sbom",
	Short:   "Print the Software Bill Of Materials of a gokrazy instance",
	Long: `gok sbom generates an SBOM of what gok overwrite or gok update would build

Examples:
  # print the hash and SBOM contents in JSON format
  % gok -i scanner sbom

  # show only the hash of the SBOM
  % gok -i scanner sbom --format hash

`,
	RunE: func(cmd *cobra.Command, args []string) error {
		return sbomImpl.run(cmd.Context(), args, cmd.OutOrStdout(), cmd.OutOrStderr())
	},
}

type sbomConfig struct {
	format string
}

var sbomImpl sbomConfig

func init() {
	sbomCmd.Flags().StringVarP(&sbomImpl.format, "format", "", "json", "output format. one of json or hash")
	instanceflag.RegisterPflags(sbomCmd.Flags())
}

func (r *sbomConfig) run(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	fileCfg, err := config.ApplyInstanceFlag()
	if err != nil {
		if os.IsNotExist(err) {
			// best-effort compatibility for old setups
			fileCfg = config.NewStruct(instanceflag.Instance())
		} else {
			return err
		}
	}

	cfg, err := config.ReadFromFile(fileCfg.Meta.Path)
	if err != nil {
		return err
	}

	if err := os.Chdir(config.InstancePath()); err != nil {
		return err
	}

	updateflag.SetUpdate("yes")

	var buf bytes.Buffer
	pack := &packer.Pack{
		// Send all build output to stderr so that stdout
		// remains fully available for printing the SBOM.
		Env: packer.Osenv{
			Stdout: &buf,
			Stderr: &buf,
		},
		FileCfg: fileCfg,
		Cfg:     cfg,
	}

	sbomMarshaled, sbomWithHash, err := pack.GenerateSBOM()
	if os.IsNotExist(err) {
		// Common case, handle with a good error message
		os.Stderr.WriteString("\n")
		log.Print(err)
		return nil
	} else if err != nil {
		stderr.Write(buf.Bytes())
		return err
	}

	if r.format == "json" {
		stdout.Write(sbomMarshaled)
	} else if r.format == "hash" {
		fmt.Fprintf(stdout, "%s\n", sbomWithHash.SBOMHash)
	} else {
		return fmt.Errorf("unknown format: expected one of json or hash")
	}

	return nil
}
