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
	"github.com/gokrazy/internal/tlsflag"
	"github.com/gokrazy/tools/gok"
	"github.com/gokrazy/tools/internal/packer"
)

type gokrazyTestInstance struct {
	name      string
	configDir string
}

func (inst *gokrazyTestInstance) configPath() string {
	return "gokrazy/" + inst.name + "/config.json"
}

func (inst *gokrazyTestInstance) readConfig(t *testing.T) config.Struct {
	b, err := os.ReadFile(inst.configPath())
	if err != nil {
		t.Fatal(err)
	}
	var cfg config.Struct
	if err := json.Unmarshal(b, &cfg); err != nil {
		t.Fatal(err)
	}
	return cfg
}

func (inst *gokrazyTestInstance) writeConfig(t *testing.T, cfg config.Struct) {
	t.Helper()
	t.Logf("Writing updated cfg = %+v", cfg)
	b, err := cfg.FormatForFile()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(inst.configPath(), b, 0644); err != nil {
		t.Fatal(err)
	}
}

func writeGokrazyInstance(t *testing.T, name string) *gokrazyTestInstance {
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
		name:      name,
		configDir: configDir,
	}
}

func TestGokUpdate(t *testing.T) {
	// Run this whole test in a throw-away temporary directory to not litter the
	// gokrazy/tools repository working copy.
	t.Chdir(t.TempDir())

	// create a new instance
	const (
		instanceName = "hello"
		hostname     = "localhost"
	)
	ti := writeGokrazyInstance(t, instanceName)

	c := gok.Context{
		Args: []string{
			"--parent_dir", "gokrazy",
			"-i", instanceName,
			"new",
		},
	}
	t.Logf("running %q", append([]string{"<gok>"}, c.Args...))
	if err := c.Execute(context.Background()); err != nil {
		t.Fatalf("%v: %v", c.Args, err)
	}

	// and update the (default) instance config for our test
	{
		cfg := ti.readConfig(t)

		// use generic kernel, enable serial console
		// TODO: use arm64 kernel when running on arm64
		kernelPackage := "github.com/gokrazy/kernel.amd64"
		cfg.KernelPackage = &kernelPackage
		cfg.FirmwarePackage = &kernelPackage
		cfg.SerialConsole = "ttyS0,115200"
		cfg.Environment = []string{"GOOS=linux", "GOARCH=amd64"}

		cfg.Hostname = hostname
		cfg.Update.Hostname = hostname
		cfg.Update.HTTPPort = "9080"
		cfg.Update.HTTPSPort = "9443"
		t.Logf("Updated cfg.Update = %+v", cfg.Update)

		ti.writeConfig(t, cfg)
	}

	t.Logf("booting gokrazy instance in a VM")
	qemu := Run(t, nil)
	defer qemu.Kill()
	// TODO: kill the test if this qemu process dies for any reason
	// test by setting an aggressive QemuOptions.Timeout

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

	// TODO: make 'gok update' not change directory?
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}

	// verify overwrite works (i.e. locates extrafiles)
	fakeBuildTimestamp := "fake-update-1"
	ctx := context.WithValue(context.Background(), packer.BuildTimestampOverride, fakeBuildTimestamp)
	c = gok.Context{
		Args: []string{
			"--parent_dir", "gokrazy",
			"-i", instanceName,
			"update",
		},
	}
	t.Logf("running %q", append([]string{"<gok>"}, c.Args...))
	if err := c.Execute(ctx); err != nil {
		t.Fatalf("%v: %v", c.Args, err)
	}

	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}

	// change to use self-signed TLS certificates
	t.Logf("Setting Update.UseTLS = self-signed")

	{
		cfg := ti.readConfig(t)

		cfg.Update.UseTLS = "self-signed"
		t.Logf("Updated cfg.Update = %+v", cfg.Update)

		ti.writeConfig(t, cfg)
	}

	fakeBuildTimestamp = "fake-update-2"
	ctx = context.WithValue(context.Background(), packer.BuildTimestampOverride, fakeBuildTimestamp)
	c = gok.Context{
		Args: []string{
			"--parent_dir", "gokrazy",
			"-i", instanceName,
			"update",
			"--insecure", // only on first update after enabling self-signed TLS
		},
	}
	t.Logf("running %q", append([]string{"<gok>"}, c.Args...))
	if err := c.Execute(ctx); err != nil {
		t.Fatalf("%v: %v", c.Args, err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}

	t.Logf("first update succeeded, doing another update without --insecure")
	fakeBuildTimestamp = "fake-update-3"
	ctx = context.WithValue(context.Background(), packer.BuildTimestampOverride, fakeBuildTimestamp)
	c = gok.Context{
		Args: []string{
			"--parent_dir", "gokrazy",
			"-i", instanceName,
			"update",
		},
	}
	t.Logf("running %q", append([]string{"<gok>"}, c.Args...))
	if err := c.Execute(ctx); err != nil {
		t.Fatalf("%v: %v", c.Args, err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}

	t.Logf("second update succeeded, doing another update after deleting the certificates (with --insecure)")
	certPath, keyPath, err := tlsflag.CertificatePathsFor(hostname)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(certPath); err != nil {
		t.Fatalf("deleting certificate: %v", err)
	}
	if err := os.Remove(keyPath); err != nil {
		t.Fatalf("deleting certificate: %v", err)
	}
	fakeBuildTimestamp = "fake-update-4"
	ctx = context.WithValue(context.Background(), packer.BuildTimestampOverride, fakeBuildTimestamp)
	c = gok.Context{
		Args: []string{
			"--parent_dir", "gokrazy",
			"-i", instanceName,
			"update",
			"--insecure", // because we deleted the certificate files
		},
	}
	t.Logf("running %q", append([]string{"<gok>"}, c.Args...))
	if err := c.Execute(ctx); err != nil {
		t.Fatalf("%v: %v", c.Args, err)
	}

}
