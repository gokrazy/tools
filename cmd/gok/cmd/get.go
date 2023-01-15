package cmd

import (
	"context"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"strings"

	"github.com/gokrazy/internal/config"
	"github.com/gokrazy/internal/instanceflag"
	"github.com/gokrazy/internal/updateflag"
	"github.com/gokrazy/tools/packer"
	"github.com/spf13/cobra"
)

// getCmd is gok get.
var getCmd = &cobra.Command{
	GroupID: "edit",
	Use:     "get",
	Short:   "Update the version of your Go program(s) using `go get`",
	Long: "gok get runs `go get` to update the version of the specified Go programs" + `
in your gokrazy instance.

Examples:
  # Update all packages on gokrazy instance scanner
  % gok -i scanner get -u

  # Update only the scan2drive program, keep gokrazy system as-is
  % gok -i scanner get github.com/stapelberg/scan2drive/cmd/scan2drive

  # Update only gokrazy system packages
  % gok -i scanner get gokrazy
`,
	RunE: func(cmd *cobra.Command, args []string) error {
		return getImpl.run(cmd.Context(), args, cmd.OutOrStdout(), cmd.OutOrStderr())
	},
}

type getImplConfig struct {
	updateAll bool
}

var getImpl getImplConfig

func init() {
	getCmd.Flags().BoolVarP(&getImpl.updateAll, "update_all", "u", false, "update all installed packages and gokrazy system packages")
	instanceflag.RegisterPflags(getCmd.Flags())
}

func getGokrazySystemPackages(cfg *config.Struct) []string {
	pkgs := append([]string{}, cfg.GokrazyPackagesOrDefault()...)
	pkgs = append(pkgs, packer.InitDeps(cfg.InternalCompatibilityFlags.InitPkg)...)
	pkgs = append(pkgs, cfg.KernelPackageOrDefault())
	if fw := cfg.FirmwarePackageOrDefault(); fw != "" {
		pkgs = append(pkgs, fw)
	}
	if e := cfg.EEPROMPackageOrDefault(); e != "" {
		pkgs = append(pkgs, e)
	}
	return pkgs
}

func (r *getImplConfig) run(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	cfg, err := config.ReadFromFile()
	if err != nil {
		if os.IsNotExist(err) {
			// best-effort compatibility for old setups
			cfg = &config.Struct{
				Hostname: instanceflag.Instance(),
			}
		} else {
			return err
		}
	}

	if err := os.Chdir(config.InstancePath()); err != nil {
		return err
	}

	updateflag.SetUpdate("yes")

	packages := args
	if r.updateAll {
		if len(packages) > 0 {
			return fmt.Errorf("use either -u or specify package arguments, not both")
		}
		packages = append(getGokrazySystemPackages(cfg), cfg.Packages...)
	} else {
		filtered := make([]string, 0, len(packages))
		for _, pkg := range packages {
			if pkg == "gokrazy" {
				// gokrazy is a special value that expands to all gokrazy system
				// packages.
				filtered = append(filtered, getGokrazySystemPackages(cfg)...)
			} else {
				filtered = append(filtered, pkg)
			}
		}
		packages = filtered
	}

	for idx, pkgAndVersion := range packages {
		pkg := pkgAndVersion
		if idx := strings.IndexByte(pkg, '@'); idx > -1 {
			pkg = pkg[:idx]
		}
		buildDir := packer.BuildDir(pkg)
		_, err := os.Stat(buildDir)
		if os.IsNotExist(err) {
			// Common case, handle with a good error message
			wd, _ := os.Getwd()
			os.Stderr.WriteString("\n")
			log.Printf("Error: build directory %q does not exist in %q", buildDir, wd)
			log.Printf("Try 'gok -i %s add %s' followed by an update.", instanceflag.Instance(), pkg)
			log.Printf("Afterwards, your 'gok get' command should work")
			return nil
		}
		if err != nil {
			return err
		}

		get := exec.CommandContext(ctx, "go", "get", pkgAndVersion)
		get.Env = packer.Env()
		get.Dir = buildDir
		get.Stdout = os.Stdout
		get.Stderr = os.Stderr
		log.Printf("updating package %d of %d: %s", idx+1, len(packages), get.Args)
		log.Printf("  in %s", buildDir)
		if err := get.Run(); err != nil {
			return fmt.Errorf("%v: %v", get.Args, err)
		}
	}

	return nil
}
