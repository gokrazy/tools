package gokupdate_test

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/gokrazy/internal/config"
	"github.com/gokrazy/tools/gok"
	"github.com/gokrazy/tools/internal/packer"
)

type gokrazyTestInstance struct {
	configDir string
}

func (inst *gokrazyTestInstance) writeConfig(t *testing.T, basename, content string) {
	t.Helper()
	fn := filepath.Join(inst.configDir, basename)
	if err := os.WriteFile(fn, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
}

func writeGokrazyInstance(t *testing.T) *gokrazyTestInstance {
	t.Helper()

	// Redirect os.UserConfigDir() to a temporary directory under our
	// control. gokrazy always uses a path under os.UserConfigDir().
	var configDir string
	switch runtime.GOOS {
	case "linux":
		configHomeDir := t.TempDir()
		os.Setenv("XDG_CONFIG_HOME", configHomeDir)
		// where linux looks:
		configDir = filepath.Join(configHomeDir, "gokrazy")

	case "darwin":
		homeDir := t.TempDir()
		os.Setenv("HOME", homeDir)
		// where darwin looks:
		configDir = filepath.Join(homeDir, "Library", "Application Support", "gokrazy")

	default:
		t.Fatalf("GOOS=%s unsupported", runtime.GOOS)
	}

	if err := os.MkdirAll(configDir, 0755); err != nil {
		t.Fatal(err)
	}

	return &gokrazyTestInstance{
		configDir: configDir,
	}
}

func TestGokUpdate(t *testing.T) {
	// Run this whole test in a throw-away temporary directory to not litter the
	// gokrazy/tools repository working copy.
	t.Chdir(t.TempDir())

	_ = writeGokrazyInstance(t)

	// create a new instance
	c := gok.Context{
		Args: []string{
			"--parent_dir", "gokrazy",
			"-i", "hello",
			"new",
		},
	}
	t.Logf("running %q", append([]string{"<gok>"}, c.Args...))
	if err := c.Execute(context.Background()); err != nil {
		t.Fatalf("%v: %v", c.Args, err)
	}

	// and update the (default) instance config for our test
	{
		const configPath = "gokrazy/hello/config.json"
		b, err := os.ReadFile(configPath)
		if err != nil {
			t.Fatal(err)
		}
		var cfg config.Struct
		if err := json.Unmarshal(b, &cfg); err != nil {
			t.Fatal(err)
		}

		// use generic kernel, enable serial console
		// TODO: use arm64 kernel when running on arm64
		kernelPackage := "github.com/gokrazy/kernel.amd64"
		cfg.KernelPackage = &kernelPackage
		cfg.FirmwarePackage = &kernelPackage
		cfg.SerialConsole = "ttyS0,115200"
		cfg.Environment = []string{"GOOS=linux", "GOARCH=amd64"}

		cfg.Update.Hostname = "localhost"
		cfg.Update.HTTPPort = "9080"
		t.Logf("Updated cfg.Update = %+v", cfg.Update)

		t.Logf("Updated cfg = %+v", cfg)
		b, err = cfg.FormatForFile()
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(configPath, b, 0644); err != nil {
			t.Fatal(err)
		}
	}

	t.Logf("booting gokrazy instance in a VM")
	qemu := Run(t, nil)
	defer qemu.Kill()

	// wait for this instance to become healthy
	//
	// TODO: include the actual build timestamp once gok overwrite returns it.
	if err := qemu.ConsoleExpect("gokrazy build timestamp "); err != nil {
		t.Fatal(err)
	}
	t.Logf("gokrazy VM booted up, waiting for network reachability")
	// poll for reachability over the network
	for start := time.Now(); time.Since(start) < 10*time.Second; time.Sleep(1 * time.Second) {
		ctx, cancel := context.WithTimeout(t.Context(), 1*time.Second)
		defer cancel()
		req, err := http.NewRequest("GET", "http://localhost:9080", nil)
		if err != nil {
			t.Fatal(err)
		}
		req = req.WithContext(ctx)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Logf("VM not yet reachable: %v", err)
			continue
		}
		if resp.StatusCode == http.StatusUnauthorized {
			t.Logf("gokrazy VM became reachable over the network")
			break
		}
	}

	// verify overwrite works (i.e. locates extrafiles)
	fakeBuildTimestamp := "fake-" + time.Now().Format(time.RFC3339)
	ctx := context.WithValue(context.Background(), packer.BuildTimestampOverride, fakeBuildTimestamp)
	c = gok.Context{
		Args: []string{
			"--parent_dir", "gokrazy",
			"-i", "hello",
			"update",
		},
	}
	t.Logf("running %q", append([]string{"<gok>"}, c.Args...))
	if err := c.Execute(ctx); err != nil {
		t.Fatalf("%v: %v", c.Args, err)
	}
}
