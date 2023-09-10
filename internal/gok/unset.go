package gok

import (
	"context"
	"fmt"
	"io"

	"github.com/gokrazy/internal/instanceflag"
	"github.com/spf13/cobra"
)

// unsetCmd is gok gus unset.
var unsetCmd = &cobra.Command{
	// GroupID: "server",
	Use:   "unset",
	Short: "unset the desired SBOM version for a machineID pattern on the remote GUS server",
	Long: `gok gus unset unsets the SBOM version for a machineID pattern on the remote GUS server

Examples:
  # check if there is unset between a local and remote GUS server's SBOM
  % gok -i scanner unset --server gus.gokrazy.org
`,
	RunE: func(cmd *cobra.Command, args []string) error {
		return unsetImpl.run(cmd.Context(), args, cmd.OutOrStdout(), cmd.OutOrStderr())
	},
}

type unsetConfig struct {
	server           string
	machineIDPattern string
}

var unsetImpl unsetConfig

func init() {
	unsetCmd.Flags().StringVarP(&unsetImpl.server, "server", "", "", "HTTP(S) URL to the server to unset against")
	unsetCmd.Flags().StringVarP(&unsetImpl.machineIDPattern, "machine_id_pattern", "", "", "The pattern to match the machineIDs to which apply the provided version")
	instanceflag.RegisterPflags(unsetCmd.Flags())
	unsetCmd.MarkFlagRequired("server")
	unsetCmd.MarkFlagRequired("machine_id_pattern")
}

func (r *unsetConfig) run(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	fmt.Println("NOT IMPLEMENTED!")

	// // TODO: finish this when gokapi has a method for unsetting.
	// gusCfg := gusapi.NewConfiguration()
	// gusCfg.BasePath = r.server
	// gusCli := gusapi.NewAPIClient(gusCfg)

	// _, _, err := gusCli.IngestApi.Ingest(ctx, &gusapi.IngestApiIngestOpts{
	// 	Body: optional.NewInterface(&gusapi.IngestRequest{
	// 		MachineIdPattern: r.machineIDPattern,
	// 		SbomHash:         "",
	// 		RegistryType:     "",
	// 		DownloadLink:     "",
	// 	}),
	// })
	// if err != nil {
	// 	return fmt.Errorf("error making update request to GUS server: %w", err)
	// }

	return nil
}
