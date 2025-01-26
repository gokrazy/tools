package gok

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime/trace"

	"github.com/gokrazy/internal/config"
	"github.com/gokrazy/internal/instanceflag"
	"github.com/gokrazy/tools/internal/packer"
	"github.com/spf13/cobra"
)

// overwriteCmd is gok overwrite.
var overwriteCmd = &cobra.Command{
	GroupID: "deploy",
	Use:     "overwrite",
	Short:   "Build and deploy a gokrazy instance to a storage device",
	Long: `Build and deploy a gokrazy instance to a storage device.

You typically need to use the gok overwrite command only once,
when first deploying your gokrazy instance. Afterwards, you can
switch to the gok update command instead for updating over the network.

Examples:
  # Overwrite the contents of the SD card sdx with gokrazy instance scan2drive:
  % gok -i scan2drive overwrite --full=/dev/sdx
`,
	RunE: func(cmd *cobra.Command, args []string) error {
		if cmd.Flags().NArg() > 0 {
			fmt.Fprint(os.Stderr, `positional arguments are not supported

`)
			return cmd.Usage()
		}

		return overwriteImpl.run(cmd.Context(), args, cmd.OutOrStdout(), cmd.OutOrStderr())
	},
}

type overwriteImplConfig struct {
	full string
	gaf  string
	boot string
	root string
	mbr  string

	sudo               string
	targetStorageBytes int

	traceFile string
}

var overwriteImpl overwriteImplConfig

func init() {
	instanceflag.RegisterPflags(overwriteCmd.Flags())
	overwriteCmd.Flags().StringVarP(&overwriteImpl.full, "full", "", "", "write a full gokrazy device image to the specified device (e.g. /dev/sdx) or path (e.g. /tmp/gokrazy.img)")
	overwriteCmd.Flags().StringVarP(&overwriteImpl.gaf, "gaf", "", "", "write a .gaf (gokrazy archive format) file to the specified path (e.g. /tmp/gokrazy.gaf)")
	overwriteCmd.Flags().StringVarP(&overwriteImpl.boot, "boot", "", "", "write the gokrazy boot file system to the specified partition (e.g. /dev/sdx1) or path (e.g. /tmp/boot.fat)")
	overwriteCmd.Flags().StringVarP(&overwriteImpl.root, "root", "", "", "write the gokrazy root file system to the specified partition (e.g. /dev/sdx2) or path (e.g. /tmp/root.squashfs)")
	overwriteCmd.Flags().StringVarP(&overwriteImpl.mbr, "mbr", "", "", "write the gokrazy master boot record (MBR) to the specified device (e.g. /dev/sdx) or path (e.g. /tmp/mbr.img). only effective if -boot is specified, too")
	overwriteCmd.Flags().StringVarP(&overwriteImpl.sudo, "sudo", "", "", "Whether to elevate privileges using sudo when required (one of auto, always, never, default auto)")
	overwriteCmd.Flags().IntVarP(&overwriteImpl.targetStorageBytes, "target_storage_bytes", "", 0, "Number of bytes which the target storage device (SD card) has. Required for using -full=<file>")
	overwriteCmd.Flags().StringVarP(&overwriteImpl.traceFile, "trace_file", "", "", "If non-empty, write a Go runtime/trace to this file (for performance analysis)")
}

func (r *overwriteImplConfig) run(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	if r.traceFile != "" {
		out, err := os.Create(r.traceFile)
		if err != nil {
			return err
		}
		defer out.Close()
		if err := trace.Start(out); err != nil {
			return err
		}
		defer trace.Stop()
	}

	fileCfg, err := config.ApplyInstanceFlag()
	if err != nil {
		return err
	}

	cfg, err := config.ReadFromFile(fileCfg.Meta.Path)
	if err != nil {
		return err
	}

	if cfg.InternalCompatibilityFlags == nil {
		cfg.InternalCompatibilityFlags = &config.InternalCompatibilityFlags{}
	}

	if r.full != "" && r.gaf != "" {
		return fmt.Errorf("cannot specify both --full and --gaf")
	}

	// gok overwrite is mutually exclusive with gok update
	cfg.InternalCompatibilityFlags.Update = ""

	// Turn all paths into absolute paths so that the output files land in the
	// current directory despite the os.Chdir() call below.
	for _, str := range []*string{&r.full, &r.gaf, &r.boot, &r.root, &r.mbr} {
		if *str != "" {
			*str, err = filepath.Abs(*str)
			if err != nil {
				return err
			}
		}
	}

	// It's guaranteed that only one is not empty.
	output := packer.OutputStruct{}
	switch {
	case r.full != "":
		output.Type = packer.OutputTypeFull
		output.Path = r.full
	case r.gaf != "":
		output.Type = packer.OutputTypeGaf
		output.Path = r.gaf
	}

	cfg.InternalCompatibilityFlags.Overwrite = r.full
	cfg.InternalCompatibilityFlags.OverwriteBoot = r.boot
	cfg.InternalCompatibilityFlags.OverwriteRoot = r.root
	cfg.InternalCompatibilityFlags.OverwriteMBR = r.mbr

	if r.sudo != "" {
		cfg.InternalCompatibilityFlags.Sudo = r.sudo
	}

	if r.targetStorageBytes > 0 {
		cfg.InternalCompatibilityFlags.TargetStorageBytes = r.targetStorageBytes
	}

	if err := os.Chdir(config.InstancePath()); err != nil {
		return err
	}

	pack := &packer.Pack{
		FileCfg: fileCfg,
		Cfg:     cfg,
		Output:  &output,
	}

	pack.Main("gokrazy gok")

	return nil
}
