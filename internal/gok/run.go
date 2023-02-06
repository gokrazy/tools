package gok

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/gokrazy/internal/config"
	"github.com/gokrazy/internal/httpclient"
	"github.com/gokrazy/internal/humanize"
	"github.com/gokrazy/internal/instanceflag"
	"github.com/gokrazy/internal/progress"
	"github.com/gokrazy/internal/updateflag"
	"github.com/gokrazy/tools/packer"
	"github.com/gokrazy/updater"
	"github.com/spf13/cobra"
)

// runCmd is gok run.
var runCmd = &cobra.Command{
	GroupID: "runtime",
	Use:     "run",
	Short:   "`go install` and run on a running gokrazy instance",
	Long: "gok run uses `go install` to build the Go program in the current directory," + `
then it stores the program in RAM of a running gokrazy instance and runs the program.

This enables a quick feedback loop when working on a program that is running on gokrazy,
without having to do a full gok update every time you only want to update a single program.

Command line arguments to the program will be combined from the instance configuration file
and the ` + "`gok run`" + ` command line, in that order.

Examples:
  % cd ~/go/src/github.com/stapelberg/scan2drive/cmd/scan2drive
  % gok -i scan2drive run

  # specify extra flags on the command line
  % gok -i scan2drive run -- -tls_autocert_hosts=scan.example.com
  `,
	RunE: func(cmd *cobra.Command, args []string) error {
		return runImpl.run(cmd.Context(), args, cmd.OutOrStdout(), cmd.OutOrStderr())
	},
}

type runImplConfig struct {
	keep bool
}

var runImpl runImplConfig

func init() {
	runCmd.Flags().BoolVarP(&runImpl.keep, "keep", "k", false, "keep temporary binary")
	instanceflag.RegisterPflags(runCmd.Flags())
}

func (r *runImplConfig) run(ctx context.Context, args []string, stdout, stderr io.Writer) error {
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

	// Get the import path of the Go package in the current directory,
	// e.g. github.com/stapelberg/scan2drive/cmd/scan2drive
	list := exec.CommandContext(ctx, "go", "list")
	list.Stderr = os.Stderr
	listb, err := list.Output()
	if err != nil {
		return fmt.Errorf("%v: %v", list.Args, err)
	}
	importPath := strings.TrimSpace(string(listb))

	// basename of the current directory
	basename := filepath.Base(importPath)

	pkgs := []string{importPath}
	var noBuildPkgs []string
	packageBuildFlags := map[string][]string{
		importPath: cfg.PackageConfig[importPath].GoBuildFlags,
	}
	packageBuildTags := map[string][]string{
		importPath: cfg.PackageConfig[importPath].GoBuildTags,
	}
	buildEnv := packer.BuildEnv{
		// Remain in the current directory instead of building in a separate,
		// per-package directory.
		BuildDir: func(string) (string, error) { return "", nil },
	}
	if err := buildEnv.Build(tmp, pkgs, packageBuildFlags, packageBuildTags, noBuildPkgs); err != nil {
		return err
	}

	httpClient, _, updateBaseUrl, err := httpclient.For(cfg)
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
		err := target.Divert(
			"/user/"+basename,
			"gok-run/"+basename,
			cfg.PackageConfig[importPath].CommandLineFlags,
			args)
		if err != nil {
			return fmt.Errorf("diverting %s: %v", basename, err)
		}
	}

	// Stop progress reporting to not mess up the following logs output.
	canc()

	// stream stdout/stderr logs
	logsCfg := &logsImplConfig{
		service: basename,
	}
	return logsCfg.run(ctx, nil, stdout, stderr)
}
