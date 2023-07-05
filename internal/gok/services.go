package gok

import (
	"context"
	"fmt"
	"io"
	"os"

	"github.com/anaskhan96/soup"
	"github.com/gokrazy/internal/config"
	"github.com/gokrazy/internal/httpclient"
	"github.com/gokrazy/internal/instanceflag"
	"github.com/gokrazy/internal/updateflag"
	"github.com/spf13/cobra"
)

// svcsCmd is gok services.
var svcsCmd = &cobra.Command{
	GroupID: "runtime",
	Use:     "services",
	Short:   "Display services from a running gokrazy service",
	Long:    `Display the installed services on a gokrazy instance`,
	RunE: func(cmd *cobra.Command, args []string) error {
		return svcsImpl.run(cmd.Context(), args, cmd.OutOrStdout(), cmd.OutOrStderr())
	},
}

type svcsImplConfig struct{}

var svcsImpl svcsImplConfig

func init() {
	instanceflag.RegisterPflags(svcsCmd.Flags())
}

func (l *svcsImplConfig) run(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	cfg, err := config.ReadFromFile()
	if err != nil {
		if os.IsNotExist(err) {
			// best-effort compatibility for old setups
			cfg = config.NewStruct(instanceflag.Instance())
		} else {
			return err
		}
	}

	updateflag.SetUpdate("yes")

	httpClient, _, svcsUrl, err := httpclient.For(cfg)
	if err != nil {
		return err
	}

	HTMLDoc, err := soup.GetWithClient(svcsUrl.String(), httpClient)
	if err != nil {
		return err
	}

	doc := soup.HTMLParse(HTMLDoc)
	services := doc.FindAll("a")

	for _, link := range services {
		if link.Text() != "gokrazy" {
			fmt.Println(link.Text())
		}
	}
	return nil
}
