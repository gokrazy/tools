package gok

import (
	"context"
	"fmt"
	"io"

	"github.com/antihax/optional"
	"github.com/gokrazy/gokapi/gusapi"
	"github.com/gokrazy/internal/instanceflag"
	"github.com/spf13/cobra"
)

// setCmd is gok gus set.
var setCmd = &cobra.Command{
	// GroupID: "server",
	Use:   "set",
	Short: "Set the desired SBOM version for a machineID pattern on the remote GUS server",
	Long: `gok gus set sets the SBOM version for a machineID pattern on the remote GUS server

Examples:
  # check if there is set between a local and remote GUS server's SBOM
  % gok -i scanner set --server gus.gokrazy.org --sbom_hash="..." --download_link="..." --registry_type="..." --machine_id_pattern=""
`,
	RunE: func(cmd *cobra.Command, args []string) error {
		return setImpl.run(cmd.Context(), args, cmd.OutOrStdout(), cmd.OutOrStderr())
	},
}

type setConfig struct {
	server           string
	sbomHash         string
	registryType     string
	downloadLink     string
	machineIDPattern string
}

var setImpl setConfig

func init() {
	setCmd.Flags().StringVarP(&setImpl.server, "server", "", "", "HTTP(S) URL to the server to set against")
	setCmd.Flags().StringVarP(&setImpl.sbomHash, "sbom_hash", "", "", "The version (SBOM Hash string) of the desired gokrazy image")
	setCmd.Flags().StringVarP(&setImpl.downloadLink, "download_link", "", "", "relative (localdisk registry) or absolute download link with which gokrazy devices can download the build")
	setCmd.Flags().StringVarP(&setImpl.registryType, "registry_type", "", "", "The type of registry on which the build is stored. see download_link")
	setCmd.Flags().StringVarP(&setImpl.machineIDPattern, "machine_id_pattern", "", "", "The pattern to match the machineIDs to which apply the provided version")
	instanceflag.RegisterPflags(setCmd.Flags())
	setCmd.MarkFlagRequired("server")
	setCmd.MarkFlagRequired("sbom_hash")
	setCmd.MarkFlagRequired("machine_id_pattern")
	setCmd.MarkFlagRequired("registry_type")
	setCmd.MarkFlagRequired("download_link")
}

func (r *setConfig) run(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	gusCfg := gusapi.NewConfiguration()
	gusCfg.BasePath = r.server
	gusCli := gusapi.NewAPIClient(gusCfg)

	_, _, err := gusCli.IngestApi.Ingest(ctx, &gusapi.IngestApiIngestOpts{
		Body: optional.NewInterface(&gusapi.IngestRequest{
			MachineIdPattern: r.machineIDPattern,
			SbomHash:         r.sbomHash,
			RegistryType:     r.registryType,
			DownloadLink:     r.downloadLink,
		}),
	})
	if err != nil {
		return fmt.Errorf("error making update request to GUS server: %w", err)
	}

	return nil
}
