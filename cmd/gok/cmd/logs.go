package cmd

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"

	"github.com/donovanhide/eventsource"
	"github.com/gokrazy/internal/httpclient"
	"github.com/gokrazy/internal/updateflag"
	"github.com/gokrazy/tools/internal/instanceflag"
	"github.com/spf13/cobra"
	"golang.org/x/sync/errgroup"
)

// logsCmd is gok logs.
var logsCmd = &cobra.Command{
	Use:   "logs",
	Short: "stream logs from gokrazy service",
	Long:  `Displays the most recent 100 log lines from stdout and stderr each, and any new lines the gokrazy service produces (cancel any time with Ctrl-C)`,
	RunE: func(cmd *cobra.Command, args []string) error {
		return logsImpl.run(cmd.Context(), args, cmd.OutOrStdout(), cmd.OutOrStderr())
	},
}

type logsImplConfig struct {
	service string
}

var logsImpl logsImplConfig

func init() {
	logsCmd.Flags().StringVarP(&logsImpl.service, "service", "s", "", "gokrazy service to fetch logs for")
	instanceflag.RegisterPflags(logsCmd.Flags())
	updateflag.RegisterPflags(logsCmd.Flags(), "update")
}

func (l *logsImplConfig) run(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	instance := instanceflag.Instance()
	if updateflag.NewInstallation() {
		updateflag.SetUpdate("yes")
	}

	if l.service == "" {
		return fmt.Errorf("the -service flag is empty, but required")
	}

	httpClient, logsUrl, err := httpclient.GetHTTPClientForInstance(instance)
	if err != nil {
		return err
	}

	q := logsUrl.Query()
	if strings.HasPrefix(l.service, "/") {
		q.Set("path", l.service)
	} else {
		q.Set("path", "/user/"+l.service)
	}
	q.Set("stream", "stdout")
	logsUrl.RawQuery = q.Encode()
	logsUrl.Path = "/log"
	stdoutUrl := logsUrl.String()
	q.Set("stream", "stderr")
	logsUrl.RawQuery = q.Encode()
	stderrUrl := logsUrl.String()

	log.Printf("streaming logs of service %q from gokrazy instance %q", l.service, instance)
	var eg errgroup.Group
	eg.Go(func() error {
		return l.streamLog(ctx, stdout, stdoutUrl, httpClient)
	})
	eg.Go(func() error {
		return l.streamLog(ctx, stderr, stderrUrl, httpClient)
	})
	if err := eg.Wait(); err != nil {
		var se eventsource.SubscriptionError
		if errors.As(err, &se) {
			if se.Code == http.StatusNotFound {
				return fmt.Errorf("service %q not found (HTTP code 404)", l.service)
			}
		}
		return err
	}
	return nil
}

func (r *logsImplConfig) streamLog(ctx context.Context, w io.Writer, url string, httpClient *http.Client) error {
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return err
	}

	stream, err := eventsource.SubscribeWith("", httpClient, req)
	if err != nil {
		return err
	}
	defer stream.Close()

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case ev := <-stream.Events:
			fmt.Fprintln(w, ev.Data())
		case err := <-stream.Errors:
			log.Printf("log streaming error: %v", err)
		}
	}
}
