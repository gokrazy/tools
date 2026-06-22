package goktest

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/gokrazy/internal/config"
)

type Instance struct {
	Name      string
	ConfigDir string
}

func (inst *Instance) configPath() string {
	return filepath.Join(inst.ConfigDir, inst.Name, "config.json")
}

func (inst *Instance) ReadConfig(t *testing.T) config.Struct {
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

func (inst *Instance) WriteConfig(t *testing.T, cfg config.Struct) {
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

func WriteInstance(t *testing.T, name string) *Instance {
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

	return &Instance{
		Name:      name,
		ConfigDir: configDir,
	}
}
