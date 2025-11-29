package gokupdate_test

import (
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"testing"
	"time"

	"github.com/anatol/vmtest"
)

// TODO: move to a gokrazy/internal/integrationtest package
func Run(t *testing.T, qemuArgs []string) *vmtest.Qemu {
	tempdir := t.TempDir()
	diskImage := filepath.Join(tempdir, "gokrazy.img")
	// diskImage := "/tmp/gokrazy.img" // for debugging

	// TODO: use in-process gok overwrite
	packer := exec.Command("gok",
		"overwrite",
		"--instance=hello",
		"--parent_dir=gokrazy",
		"--full="+diskImage,
		"--target_storage_bytes="+strconv.Itoa(2*1024*1024*1024))
	packer.Env = append(os.Environ(), "GOARCH=amd64")
	packer.Stdout = os.Stdout
	packer.Stderr = os.Stderr
	log.Printf("%s", packer.Args)
	if err := packer.Run(); err != nil {
		t.Fatalf("%s: %v", packer.Args, err)
	}

	// Chosen to match internal/gok/vmrun.go
	qemuArgs = append(qemuArgs,
		//"-enable-kvm",
		//"-cpu", "host",
		"-nodefaults",
		"-m", "1024",
		// required! system gets stuck without -smp
		"-smp", strconv.Itoa(max(runtime.NumCPU(), 2)),
		"-device", "e1000,netdev=net0",
		"-netdev", "user,id=net0,hostfwd=tcp::9080-:9080,hostfwd=tcp::9022-:22",
		// Use -drive instead of vmtest.QemuOptions.Disks because the latter
		// results in wiring up the devices using SCSI in a way that the
		// router7 kernel config does not support.
		// TODO: update kernel config and switch to Disks:
		"-boot", "order=d",
		"-drive", "file="+diskImage+",format=raw",
	)

	opts := vmtest.QemuOptions{
		Architecture:    vmtest.QEMU_X86_64,
		OperatingSystem: vmtest.OS_LINUX,
		Params:          qemuArgs,
		// Disks: []vmtest.QemuDisk{
		// 	{
		// 		Path:   diskImage,
		// 		Format: "raw",
		// 	},
		// },
		Verbose: testing.Verbose(),
		Timeout: 1 * time.Minute,
	}
	qemu, err := vmtest.NewQemu(&opts)
	if err != nil {
		t.Fatal(err)
	}
	return qemu
}
