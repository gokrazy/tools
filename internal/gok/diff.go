package gok

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"strings"

	"github.com/antihax/optional"
	"github.com/gokrazy/gokapi/gusapi"
	"github.com/gokrazy/internal/config"
	"github.com/gokrazy/internal/instanceflag"
	"github.com/spf13/cobra"
)

// diffCmd is gok gus diff.
var diffCmd = &cobra.Command{
	// GroupID: "server",
	Use:   "diff",
	Short: "Compare the SBOM on disk with the one set on the remote GUS server",
	Long: `gok gus diff compares the SBOM of the gokrazy instance on disk with the one currently set on the remote GUS server

Examples:
  # check if there is diff between a local and remote GUS server's SBOM
  % gok -i scanner diff --server gus.gokrazy.org
`,
	RunE: func(cmd *cobra.Command, args []string) error {
		return diffImpl.run(cmd.Context(), args, cmd.OutOrStdout(), cmd.OutOrStderr())
	},
}

type diffConfig struct {
	server string
}

var diffImpl diffConfig

func init() {
	diffCmd.Flags().StringVarP(&diffImpl.server, "server", "", "", "HTTP(S) URL to the server to diff against")
	instanceflag.RegisterPflags(diffCmd.Flags())
	diffCmd.MarkFlagRequired("server")
}

func (r *diffConfig) run(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	cfg, err := config.ReadFromFile()
	if err != nil {
		return err
	}

	machineID, err := getMachineID(cfg)
	if err != nil {
		return fmt.Errorf("error getting machineID: %w", err)
	}

	sc := sbomConfig{format: "hash"}
	stdOutbuf := new(bytes.Buffer)
	if err := sc.run(ctx, nil, stdOutbuf, nil); err != nil {
		return err
	}
	localSBOMHash := strings.TrimSpace(stdOutbuf.String())

	gusCfg := gusapi.NewConfiguration()
	gusCfg.BasePath = r.server
	gusCli := gusapi.NewAPIClient(gusCfg)

	response, _, err := gusCli.UpdateApi.Update(ctx, &gusapi.UpdateApiUpdateOpts{
		Body: optional.NewInterface(&gusapi.UpdateRequest{
			MachineId: machineID,
		}),
	})
	if err != nil {
		return fmt.Errorf("error making update request to GUS server: %w", err)
	}

	if localSBOMHash != response.SbomHash {
		return fmt.Errorf("local: %s != remote: %s", localSBOMHash, response.SbomHash)
	}

	return nil
}

func getMachineID(cfg *config.Struct) (string, error) {
	if cfg == nil {
		return "", fmt.Errorf("error reading nil config")
	}
	v, ok := cfg.PackageConfig["github.com/gokrazy/gokrazy/cmd/randomd"]
	if !ok {
		return "", fmt.Errorf("error undefined machineID")
	}
	rawMachineID, ok := v.ExtraFileContents["/etc/machine-id"]
	if !ok {
		return "", fmt.Errorf("error undefined machineID")
	}

	return strings.TrimSpace(rawMachineID), nil
}
