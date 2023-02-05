package gok

import (
	"context"
	"fmt"
	"io"
	"os"

	"github.com/gokrazy/internal/config"
	"github.com/gokrazy/internal/instanceflag"
	"github.com/gokrazy/tools/internal/packer"
	"github.com/spf13/cobra"
)

// updateCmd is gok update.
var updateCmd = &cobra.Command{
	GroupID: "deploy",
	Use:     "update",
	Short:   "Build a gokrazy instance and update over the network",
	Long: `Build a gokrazy instance and update over the network.
`,
	RunE: func(cmd *cobra.Command, args []string) error {
		if cmd.Flags().NArg() > 0 {
			fmt.Fprint(os.Stderr, `positional arguments are not supported

`)
			return cmd.Usage()
		}

		return updateImpl.run(cmd.Context(), args, cmd.OutOrStdout(), cmd.OutOrStderr())
	},
}

type updateImplConfig struct {
	insecure bool
	testboot bool
}

var updateImpl updateImplConfig

func init() {
	instanceflag.RegisterPflags(updateCmd.Flags())
	updateCmd.Flags().BoolVarP(&updateImpl.insecure, "insecure", "", false, "Disable TLS stripping detection. Should only be used when first enabling TLS, not permanently.")
	updateCmd.Flags().BoolVarP(&updateImpl.testboot, "testboot", "", false, "Trigger a testboot instead of switching to the new root partition directly")
}

func (r *updateImplConfig) run(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	cfg, err := config.ReadFromFile()
	if err != nil {
		return err
	}

	if cfg.InternalCompatibilityFlags == nil {
		cfg.InternalCompatibilityFlags = &config.InternalCompatibilityFlags{}
	}

	// gok update is mutually exclusive with gok overwrite
	cfg.InternalCompatibilityFlags.Overwrite = ""
	cfg.InternalCompatibilityFlags.OverwriteBoot = ""
	cfg.InternalCompatibilityFlags.OverwriteRoot = ""
	cfg.InternalCompatibilityFlags.OverwriteMBR = ""

	if cfg.InternalCompatibilityFlags.Update == "" {
		cfg.InternalCompatibilityFlags.Update = "yes"
	}

	if r.insecure {
		cfg.InternalCompatibilityFlags.Insecure = true
	}

	if r.testboot {
		cfg.InternalCompatibilityFlags.Testboot = true
	}

	if err := os.Chdir(config.InstancePath()); err != nil {
		return err
	}

	pack := &packer.Pack{
		Cfg: cfg,
	}

	pack.Main("gokrazy gok")

	return nil
}
