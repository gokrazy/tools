package cmd

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"strings"

	"github.com/donovanhide/eventsource"
	"github.com/gokrazy/internal/config"
	"github.com/gokrazy/internal/updateflag"
	"github.com/spf13/cobra"
	"golang.org/x/sync/errgroup"
)

// logsCmd is gok logs.
var logsCmd = &cobra.Command{
	Use:   "logs",
	Short: "stream logs from gokrazy service",
	Long:  `Displays the most recent 100 log lines from stdout and stderr each, and any new lines the gokrazy service produces (cancel any time with Ctrl-C)`,
	Run: func(cmd *cobra.Command, args []string) {
		if err := logsImpl.run(context.Background(), args); err != nil {
			log.Fatal(err)
		}
	},
}

type logsImplConfig struct {
	service  string
	instance string
}

var logsImpl logsImplConfig

func init() {
	logsCmd.Flags().StringVarP(&logsImpl.service, "service", "s", "", "gokrazy service to fetch logs for")
	logsCmd.Flags().StringVarP(&logsImpl.instance, "instance", "i", "gokrazy", "instance, identified by hostname")
	updateflag.RegisterPflags(logsCmd.Flags())
}

func (l *logsImplConfig) run(ctx context.Context, args []string) error {
	if updateflag.NewInstallation() {
		updateflag.SetUpdate("yes")
	}

	if l.service == "" {
		return fmt.Errorf("the -service flag is empty, but required")
	}

	// copy the binary over to the running installation
	_, updateHostname := updateflag.GetUpdateTarget(l.instance)
	const configBaseName = "http-password.txt"
	pw, err := config.HostnameSpecific(updateHostname).ReadFile(configBaseName)
	if err != nil {
		return err
	}
	logsUrl, err := updateflag.BaseURL("80", "http", updateHostname, pw)
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

	log.Printf("streaming logs of service %q from gokrazy instance %q", l.service, updateHostname)
	var eg errgroup.Group
	eg.Go(func() error {
		return l.streamLog(ctx, os.Stdout, stdoutUrl)
	})
	eg.Go(func() error {
		return l.streamLog(ctx, os.Stderr, stderrUrl)
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

func (r *logsImplConfig) streamLog(ctx context.Context, w io.Writer, url string) error {
	stream, err := eventsource.Subscribe(url, "")
	if err != nil {
		return err
	}
	defer stream.Close()
	for {
		select {
		case ev := <-stream.Events:
			fmt.Fprintln(w, ev.Data())
		case err := <-stream.Errors:
			log.Printf("log streaming error: %v", err)
		}
	}
}
