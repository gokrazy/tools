package gok

import (
	"context"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"

	"github.com/gokrazy/internal/config"
	"github.com/gokrazy/internal/instanceflag"
	"github.com/gokrazy/tools/internal/packer"
	"github.com/spf13/cobra"
)

var vmRunCmd = &cobra.Command{
	Use:   "run",
	Short: "run a virtual machine (using QEMU)",
	Long: `gok run builds a gokrazy instance and runs it using QEMU.

Examples:
  % gok vm run

  # Boot directly into a serial console in your terminal
  # (Use C-a x to exit.)
  % gok vm run --graphic=false
`,
	RunE: func(cmd *cobra.Command, args []string) error {
		return vmRunImpl.run(cmd.Context(), args, cmd.OutOrStdout(), cmd.OutOrStderr())
	},
}

func init() {
	vmCmd.AddCommand(vmRunCmd)
}

type vmRunConfig struct {
	dry                bool
	keep               bool
	graphic            bool
	sudo               string
	targetStorageBytes int
	arch               string
}

var vmRunImpl vmRunConfig

func init() {
	vmRunCmd.Flags().StringVarP(&vmRunImpl.sudo, "sudo", "", "", "Whether to elevate privileges using sudo when required (one of auto, always, never, default auto)")
	vmRunCmd.Flags().IntVarP(&vmRunImpl.targetStorageBytes, "target_storage_bytes", "", 1258299392, "Size of the disk image in bytes")
	vmRunCmd.Flags().StringVarP(&vmRunImpl.arch, "arch", "", runtime.GOARCH, "architecture for which to build and run QEMU. One of 'amd64' or 'arm64'")
	vmRunCmd.Flags().BoolVarP(&vmRunImpl.keep, "keep", "", false, "keep ephemeral disk images around instead of deleting them when QEMU exits")
	vmRunCmd.Flags().BoolVarP(&vmRunImpl.dry, "dryrun", "", false, "Whether to actually run QEMU or merely print the command")
	vmRunCmd.Flags().BoolVarP(&vmRunImpl.graphic, "graphic", "", true, "Run QEMU in graphical mode?")
	instanceflag.RegisterPflags(vmRunCmd.Flags())
}

func (r *vmRunConfig) buildFullDiskImage(ctx context.Context, dest string) error {
	os.Setenv("GOARCH", r.arch)

	fileCfg, err := config.ReadFromFile()
	if err != nil {
		return err
	}

	cfg, err := config.ReadFromFile()
	if err != nil {
		return err
	}

	if cfg.InternalCompatibilityFlags == nil {
		cfg.InternalCompatibilityFlags = &config.InternalCompatibilityFlags{}
	}

	// gok overwrite is mutually exclusive with gok update
	cfg.InternalCompatibilityFlags.Update = ""

	// Turn all paths into absolute paths so that the output files land in the
	// current directory despite the os.Chdir() call below.
	if dest != "" {
		dest, err = filepath.Abs(dest)
		if err != nil {
			return err
		}
	}

	// It's guaranteed that only one is not empty.
	output := packer.OutputStruct{}

	output.Type = packer.OutputTypeFull
	output.Path = dest

	cfg.InternalCompatibilityFlags.Overwrite = dest

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

func (r *vmRunConfig) runQEMU(ctx context.Context, fullDiskImage string) error {
	qemuBin := "qemu-system-x86_64"
	switch r.arch {
	case "amd64":
		// default
	case "arm64":
		qemuBin = "qemu-system-aarch64"
	}

	qemu := exec.CommandContext(ctx, qemuBin,
		"-name", instanceflag.Instance(),
		"-boot", "order=d",
		"-drive", "file="+fullDiskImage+",format=raw",
		"-device", "i6300esb,id=watchdog0",
		"-watchdog-action", "reset",
		"-smp", strconv.Itoa(max(runtime.NumCPU(), 2)),
		"-device", "e1000,netdev=net0",
		"-netdev", "user,id=net0,hostfwd=tcp::8080-:80,hostfwd=tcp::8022-:22",
		"-m", "1024")

	// Start in EFI mode (not legacy BIOS) so that we get a frame buffer (for
	// gokrazy’s fbstatus program) and serial console.
	switch r.arch {
	case "arm64":
		qemu.Args = append(qemu.Args,
			"-machine", "virt,highmem=off",
			"-cpu", "cortex-a72")
		// TODO: set -bios to an embedded copy of qemu-efi-aarch64/QEMU_EFI.fd

	case "amd64":
		for _, location := range []string{
			// Debian, Fedora
			"/usr/share/OVMF/OVMF_CODE.fd",
			// Arch Linux
			"/usr/share/edk2-ovmf/x64/OVMF_CODE.fd",
		} {
			if _, err := os.Stat(location); err == nil {
				fmt.Printf("starting in UEFI mode, OVMF found at %s\n", location)
				qemu.Args = append(qemu.Args, "-bios", location)
				break
			}
		}
	}

	if r.arch == runtime.GOARCH {
		// Hardware acceleration (in both cases) is only available for the
		// native architecture, e.g. arm64 for M1 MacBooks.
		switch runtime.GOOS {
		case "linux":
			qemu.Args = append(qemu.Args, "-accel", "kvm")
		case "darwin":
			qemu.Args = append(qemu.Args, "-accel", "hvf")
		}
	}

	if !r.graphic {
		qemu.Args = append(qemu.Args, "-nographic")
	}

	qemu.Stdin = os.Stdin
	qemu.Stdout = os.Stdout
	qemu.Stderr = os.Stderr
	fmt.Printf("%s\n", qemu.Args)
	if !r.dry {
		if err := qemu.Run(); err != nil {
			return fmt.Errorf("%v: %v", qemu.Args, err)
		}
	}
	return nil
}

func (r *vmRunConfig) run(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	f, err := os.CreateTemp("", "gokrazy-vm")
	if err != nil {
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	fdi := f.Name()
	log.Printf("building disk image")
	if !r.dry {
		if err := r.buildFullDiskImage(ctx, fdi); err != nil {
			return err
		}
	}

	log.Printf("running QEMU")
	if err := r.runQEMU(ctx, fdi); err != nil {
		return err
	}

	if !r.keep {
		log.Printf("deleting full disk image, use --keep to keep it around")
		return os.Remove(fdi)
	}

	return nil
}
