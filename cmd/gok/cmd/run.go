package cmd

import (
	"context"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"time"

	"github.com/gokrazy/internal/httpclient"
	"github.com/gokrazy/internal/humanize"
	"github.com/gokrazy/internal/progress"
	"github.com/gokrazy/internal/updateflag"
	"github.com/gokrazy/tools/packer"
	"github.com/gokrazy/updater"
	"github.com/spf13/cobra"
)

// runCmd is gok run.
var runCmd = &cobra.Command{
	Use:   "run",
	Short: "`go install` and run on gokrazy",
	Long:  `do iit`,
	RunE: func(cmd *cobra.Command, args []string) error {
		return runImpl.run(cmd.Context(), args, cmd.OutOrStdout(), cmd.OutOrStderr())
	},
}

type runImplConfig struct {
	keep     bool
	instance string
}

var runImpl runImplConfig

func init() {
	runCmd.Flags().BoolVarP(&runImpl.keep, "keep", "k", false, "keep temporary binary")
	runCmd.Flags().StringVarP(&runImpl.instance, "instance", "i", "gokrazy", "instance, identified by hostname")
	updateflag.RegisterPflags(runCmd.Flags(), "update")
}

func (r *runImplConfig) run(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	if updateflag.NewInstallation() {
		updateflag.SetUpdate("yes")
	}

	var tmp string
	if r.keep {
		tmp = os.TempDir()
	} else {
		var err error
		tmp, err = os.MkdirTemp("", "gokrazy-bins-")
		if err != nil {
			return err
		}
		defer os.RemoveAll(tmp)
	}

	// basename of the current directory
	wd, err := os.Getwd()
	if err != nil {
		return err
	}
	basename := filepath.Base(wd)
	log.Printf("basename: %q", basename)

	pkgs := []string{
		".", // build what is in the current directory
	}
	var noBuildPkgs []string
	// TODO: gather packageBuildFlags
	var packageBuildFlags map[string][]string
	// TODO: gather packageBuildTags
	var packageBuildTags map[string][]string
	buildEnv := packer.BuildEnv{
		// Remain in the current directory instead of building in a separate,
		// per-package directory.
		BuildDir: func(string) (string, error) { return "", nil },
	}
	if err := buildEnv.Build(tmp, pkgs, packageBuildFlags, packageBuildTags, noBuildPkgs); err != nil {
		return err
	}

	httpClient, updateBaseUrl, err := httpclient.GetHTTPClientForInstance(r.instance)
	if err != nil {
		return err
	}

	target, err := updater.NewTarget(updateBaseUrl.String(), httpClient)
	if err != nil {
		return fmt.Errorf("checking target partuuid support: %v", err)
	}

	progctx, canc := context.WithCancel(ctx)
	defer canc()
	prog := &progress.Reporter{}
	go prog.Report(progctx)

	f, err := os.Open(filepath.Join(tmp, basename))
	if err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("binary %s not installed; are you not in a directory where .go files declare “package main”?", basename)
		}
		return err
	}
	defer f.Close()

	prog.SetStatus("uploading " + basename)
	if st, err := f.Stat(); err == nil {
		prog.SetTotal(uint64(st.Size()))
	}

	{
		start := time.Now()
		err := target.Put("uploadtemp/gok-run/"+basename, io.TeeReader(f, &progress.Writer{}))
		if err != nil {
			return fmt.Errorf("uploading temporary binary: %v", err)
		}
		duration := time.Since(start)
		transferred := progress.Reset()
		fmt.Printf("\rTransferred %s (%s) at %.2f MiB/s (total: %v)\n",
			basename,
			humanize.Bytes(transferred),
			float64(transferred)/duration.Seconds()/1024/1024,
			duration.Round(time.Second))

	}

	// Make gokrazy use the temporary binary instead of
	// /user/<basename>. Includes an automatic service restart.
	{
		if err := target.Divert("/user/"+basename, "gok-run/"+basename); err != nil {
			return fmt.Errorf("diverting %s: %v", basename, err)
		}
	}

	// Stop progress reporting to not mess up the following logs output.
	canc()

	// stream stdout/stderr logs
	logsCfg := &logsImplConfig{
		service:  basename,
		instance: r.instance,
	}
	return logsCfg.run(ctx, nil, stdout, stderr)
}
