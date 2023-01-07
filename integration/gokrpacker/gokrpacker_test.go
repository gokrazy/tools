package gokrpacker_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gokrazy/tools/cmd/gok/cmd"
	"github.com/gokrazy/tools/internal/oldpacker"
	"github.com/google/go-cmp/cmp"
)

func TestMain(m *testing.M) {
	if os.Getenv("EXEC_GOKR_PACKER") == "1" {
		oldpacker.Main()
		return
	}
	if os.Getenv("EXEC_GOK") == "1" {
		cmd.Execute()
		return
	}
	os.Exit(m.Run())
}

func unsquashList(t *testing.T, path string) []string {
	t.Helper()
	unsquashfs := exec.Command("unsquashfs", "-l", path)
	unsquashfs.Stderr = os.Stderr
	out, err := unsquashfs.Output()
	if err != nil {
		t.Fatal(err)
	}
	return strings.Split(strings.TrimSpace(string(out)), "\n")
}

func TestGokrPacker(t *testing.T) {
	// While gok is the preferred new tool for using gokrazy, the gokr-packer
	// tool should still keep working, at least for a while. This integration
	// test ensures we don’t catastrophically break it.

	output := t.TempDir()
	os.Setenv("GOKRAZY_PARENT_DIR", output)

	// Run the gokr-packer code by running our own executable with
	// EXEC_GOKR_PACKER=1 set, which runs the gokr-packer logic.
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	rootSquashfs := filepath.Join(output, "root.squashfs")
	bootFat := filepath.Join(output, "boot.fat")
	packer := exec.Command(exe,
		"-overwrite_root="+rootSquashfs,
		"-overwrite_boot="+bootFat,
		"-target_storage_bytes=1610612736",
		"github.com/gokrazy/hello")
	packer.Dir = output
	packer.Env = append(os.Environ(), "EXEC_GOKR_PACKER=1")
	packer.Stdout = os.Stdout
	packer.Stderr = os.Stderr
	t.Logf("running %q", packer.Args)
	if err := packer.Run(); err != nil {
		t.Fatalf("%v: %v", packer.Args, err)
	}

	rootFilesGokrPacker := unsquashList(t, rootSquashfs)

	// delete root.squashfs and boot.fat to ensure the migration test re-creates it
	if err := os.Remove(rootSquashfs); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(bootFat); err != nil {
		t.Fatal(err)
	}

	t.Run("MigrationToGok", func(t *testing.T) {
		// TODO: try with flag at very end, too?
		migrate := exec.Command(exe, append([]string{"-write_instance_config=hello"}, packer.Args[1:]...)...)
		migrate.Dir = packer.Dir
		migrate.Env = append(os.Environ(), "EXEC_GOKR_PACKER=1")
		migrate.Stdout = os.Stdout
		migrate.Stderr = os.Stderr
		t.Logf("running %q", migrate.Args)
		if err := migrate.Run(); err != nil {
			t.Fatalf("%v: %v", migrate.Args, err)
		}

		configb, err := os.ReadFile(filepath.Join(output, "hello", "config.json"))
		if err != nil {
			t.Fatal(err)
		}
		t.Logf("config.json:\n%s", strings.TrimSpace(string(configb)))

		overwrite := exec.Command(exe, "overwrite", "--root=root.squashfs", "--boot=boot.fat")
		overwrite.Dir = packer.Dir
		overwrite.Env = append(os.Environ(), "EXEC_GOK=1")
		overwrite.Stdout = os.Stdout
		overwrite.Stderr = os.Stderr
		t.Logf("running %q", overwrite.Args)
		if err := overwrite.Run(); err != nil {
			t.Fatalf("%v: %v", overwrite.Args, err)
		}

		rootFilesGok := unsquashList(t, rootSquashfs)
		if diff := cmp.Diff(rootFilesGokrPacker, rootFilesGok); diff != "" {
			t.Fatalf("gok overwrite produced different root file system compared to gokr-packer: diff (-packer +gok):\n%s", diff)
		}

		if err := os.Remove(rootSquashfs); err != nil {
			t.Fatal(err)
		}
		if err := os.Remove(bootFat); err != nil {
			t.Fatal(err)
		}
	})
}
