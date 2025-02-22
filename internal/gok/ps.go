package gok

import (
	"context"
	"fmt"
	"io"
	"os"

	"github.com/gokrazy/gokapi"
	"github.com/gokrazy/gokapi/ondeviceapi"
	"github.com/gokrazy/internal/config"
	"github.com/gokrazy/internal/instanceflag"
	"github.com/spf13/cobra"
)

// psCmd is gok ps.
var psCmd = &cobra.Command{
	GroupID: "runtime",
	Use:     "ps",
	Short:   "list processes of a running gokrazy instance",
	Long: `gok ps

Examples:
  % gok -i scan2drive ps
  `,
	RunE: func(cmd *cobra.Command, args []string) error {
		return psImpl.run(cmd.Context(), args, cmd.OutOrStdout(), cmd.OutOrStderr())
	},
}

type psImplConfig struct {
}

var psImpl psImplConfig

func init() {
	instanceflag.RegisterPflags(psCmd.Flags())
}

func (r *psImplConfig) run(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	cfg, err := config.ApplyInstanceFlag()
	if err != nil {
		if os.IsNotExist(err) {
			// best-effort compatibility for old setups
			cfg = config.NewStruct(instanceflag.Instance())
		} else {
			return err
		}
	}

	acfg, err := gokapi.ConnectRemotely(cfg)
	if err != nil {
		return err
	}
	cl := ondeviceapi.NewAPIClient(acfg)
	index, resp, err := cl.SuperviseApi.Index(ctx)
	if err != nil {
		return err
	}
	_ = resp
	fmt.Printf("Host:   %s\n", cfg.Hostname)
	fmt.Printf("Model:  %s\n", index.Model)
	fmt.Printf("Build:  %s\n", index.BuildTimestamp)
	fmt.Printf("Kernel: %s\n", index.Kernel)
	fmt.Printf("Services (%d):\n", len(index.Services))
	for _, svc := range index.Services {
		fmt.Printf("  %s\n", svc.Path)
	}

	return nil
}
