package gok

import (
	"context"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/gokrazy/internal/instanceflag"
	"github.com/spf13/cobra"
)

// pushCmd is gok push.
var pushCmd = &cobra.Command{
	GroupID: "server",
	Use:     "push",
	Short:   "Push a gokrazy image to a remote GUS server",
	Long: `gok push pushes a local gaf (gokrazy archive format) file to a remote server.

When the --json flag is specified, the server response is printed to stdout.

Examples:
  # push gokrazy.gaf to the GUS server at gus.gokrazy.org
  % gok push --gaf /tmp/gokrazy.gaf --server https://gus.gokrazy.org

`,
	RunE: func(cmd *cobra.Command, args []string) error {
		return pushImpl.run(cmd.Context(), args, cmd.OutOrStdout(), cmd.OutOrStderr())
	},
}

type pushConfig struct {
	gafPath string
	server  string
	json    bool
}

var pushImpl pushConfig

func init() {
	pushCmd.Flags().StringVarP(&pushImpl.gafPath, "gaf", "", "", "path to the .gaf (gokrazy archive format) file to push to GUS (e.g. /tmp/gokrazy.gaf); build using gok overwrite --gaf")
	pushCmd.Flags().StringVarP(&pushImpl.server, "server", "", "", "HTTP(S) URL to the server to push to")
	pushCmd.Flags().BoolVarP(&pushImpl.json, "json", "", false, "print server JSON response directly to stdout")
	instanceflag.RegisterPflags(pushCmd.Flags())
}

func (r *pushConfig) run(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	// TODO: use an io.Reader that allows us to indicate progress
	body, err := os.Open(r.gafPath)
	if err != nil {
		return err
	}
	defer body.Close()
	st, err := body.Stat()
	if err != nil {
		return err
	}

	// TODO: fall back to the GUS server in the instance config if r.server == ""
	start := time.Now()
	url := strings.TrimSuffix(r.server, "/") + "/api/v1/push"
	req, err := http.NewRequestWithContext(ctx, "PUT", url, body)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/octet-stream")
	log.Printf("pushing %s (%d bytes) to %s", r.gafPath, st.Size(), url)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	if got, want := resp.StatusCode, http.StatusOK; got != want {
		return fmt.Errorf("unexpected HTTP status: got %v, want %v", resp.Status, want)
	}
	if r.json {
		if _, err := io.Copy(os.Stdout, resp.Body); err != nil {
			return err
		}
	} else {
		dur := time.Since(start)
		log.Printf("uploaded in %v (%.f MB/s)",
			dur.Truncate(1*time.Millisecond),
			float64(st.Size()/1024/1024)/dur.Seconds())
	}

	return nil
}
