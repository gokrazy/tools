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
	edk "github.com/gokrazy/tools/third_party/edk2-2024.11-4"
	"github.com/spf13/cobra"
)

var vmRunCmd = &cobra.Command{
	Use:   "run",
	Short: "run a virtual machine (using QEMU)",
	Long: `gok run builds a gokrazy instance and runs it using QEMU.

Extra arguments are passed to QEMU as-is.

Examples:
  % gok vm run

  # Boot directly into a serial console in your terminal
  # (Use C-a x to exit.)
  % gok vm run --graphic=false

  # Directly specify QEMU USB flags
  % gok vm run -- -usb -device usb-mouse
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
	netdev             string
}

func (r *vmRunConfig) effectiveGoarch() string {
	goarch := os.Getenv("GOARCH")
	if goarch != "" {
		return goarch
	}
	return runtime.GOARCH
}

var vmRunImpl vmRunConfig

func init() {
	vmRunCmd.Flags().StringVarP(&vmRunImpl.sudo, "sudo", "", "", "Whether to elevate privileges using sudo when required (one of auto, always, never, default auto)")
	const permSize = 512 * 1024 * 1024
	vmRunCmd.Flags().IntVarP(&vmRunImpl.targetStorageBytes, "target_storage_bytes", "", 1258299392+permSize, "Size of the disk image in bytes")
	vmRunCmd.Flags().StringVarP(&vmRunImpl.arch, "arch", "", "", "architecture for which to build and run QEMU. One of 'amd64' or 'arm64'")
	vmRunCmd.Flags().StringVarP(&vmRunImpl.netdev, "netdev", "", "user,id=net0,hostfwd=tcp::8080-:80,hostfwd=tcp::8022-:22", "QEMU -netdev argument")
	vmRunCmd.Flags().BoolVarP(&vmRunImpl.keep, "keep", "", false, "keep ephemeral disk images around instead of deleting them when QEMU exits")
	vmRunCmd.Flags().BoolVarP(&vmRunImpl.dry, "dryrun", "", false, "Whether to actually run QEMU or merely print the command")
	vmRunCmd.Flags().BoolVarP(&vmRunImpl.graphic, "graphic", "", true, "Run QEMU in graphical mode?")
	instanceflag.RegisterPflags(vmRunCmd.Flags())
}

func (r *vmRunConfig) buildFullDiskImage(ctx context.Context, dest string) error {
	fileCfg, err := config.ApplyInstanceFlag()
	if err != nil {
		return err
	}

	if r.arch != "" {
		os.Setenv("GOARCH", r.arch)
	}

	cfg, err := config.ReadFromFile(fileCfg.Meta.Path)
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
		Env: packer.Osenv{
			Stdout: os.Stdout,
			Stderr: os.Stderr,
		},
		FileCfg: fileCfg,
		Cfg:     cfg,
		Output:  &output,
	}

	pack.Main("gokrazy gok")

	return nil
}

func (r *vmRunConfig) runQEMU(ctx context.Context, fullDiskImage string, extraArgs []string) error {
	tmp, err := os.MkdirTemp("", "gokrazy-vm")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmp)
	amd64OVMFCODE4M := filepath.Join(tmp, "amd64-OVMF_CODE_4M.fd")
	if err := os.WriteFile(amd64OVMFCODE4M, edk.Amd64OVMFCODE4M, 0644); err != nil {
		return err
	}
	amd64OVMFVARS4M := filepath.Join(tmp, "amd64-OVMF_VARS_4M.fd")
	if err := os.WriteFile(amd64OVMFVARS4M, edk.Amd64OVMFVARS4M, 0644); err != nil {
		return err
	}
	arm64EFI := filepath.Join(tmp, "arm64-QEMU_EFI.fd")
	if err := os.WriteFile(arm64EFI, edk.Arm64EFI, 0644); err != nil {
		return err
	}

	goarch := r.effectiveGoarch()
	qemuBin := "qemu-system-x86_64"
	switch goarch {
	case "amd64":
		// default
	case "arm64":
		qemuBin = "qemu-system-aarch64"
	}

	qemu := exec.CommandContext(ctx, qemuBin,
		append([]string{
			"-name", instanceflag.Instance(),
			"-boot", "order=d",
			"-drive", "file=" + fullDiskImage + ",format=raw",
			"-device", "i6300esb,id=watchdog0",
			"-watchdog-action", "reset",
			"-smp", strconv.Itoa(max(runtime.NumCPU(), 2)),
			"-device", "e1000,netdev=net0",
			"-netdev", r.netdev,
			"-m", "1024",
		}, extraArgs...)...)

	// Start in EFI mode (not legacy BIOS) so that we get a frame buffer (for
	// gokrazy’s fbstatus program) and serial console.
	switch goarch {
	case "arm64":
		qemu.Args = append(qemu.Args,
			"-machine", "virt,highmem=off",
			"-cpu", "cortex-a72",
			"-bios", arm64EFI)

	case "amd64":
		qemu.Args = append(qemu.Args,
			// -bios with unified 2M firmware images was deprecated in favor of
			// two pflash -drive lines with separated CODE/VARS 4M images.
			// for details see:
			//  https://salsa.debian.org/qemu-team/edk2/-/blob/debian/latest/debian/howto-2M-to-4M-migration.md
			"-drive", "if=pflash,format=raw,unit=0,readonly=on,file="+amd64OVMFCODE4M,
			"-drive", "if=pflash,format=raw,unit=1,readonly=off,file="+amd64OVMFVARS4M,
		)
	}

	if goarch == runtime.GOARCH {
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
	if err := r.runQEMU(ctx, fdi, args); err != nil {
		return err
	}

	if !r.keep {
		log.Printf("deleting full disk image, use --keep to keep it around")
		return os.Remove(fdi)
	}

	return nil
}
