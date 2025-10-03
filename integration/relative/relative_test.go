package relative_test

import (
	"context"
	"encoding/json"
	"os"
	"testing"

	"github.com/gokrazy/internal/config"
	"github.com/gokrazy/tools/gok"
)

func TestRelativeParentDir(t *testing.T) {
	// Run this whole test in a throw-away temporary directory to not litter the
	// gokrazy/tools repository working copy.
	t.Chdir(t.TempDir())

	// create a new instance
	c := gok.Context{
		Args: []string{
			"--parent_dir", "packaging/gokrazy",
			"-i", "evcc",
			"new",
		},
	}
	t.Logf("running %q", append([]string{"<gok>"}, c.Args...))
	if err := c.Execute(context.Background()); err != nil {
		t.Fatalf("%v: %v", c.Args, err)
	}

	// verify the breakglass.authorized_keys path is relative to the instance
	b, err := os.ReadFile("packaging/gokrazy/evcc/config.json")
	if err != nil {
		t.Fatal(err)
	}
	var cfg config.Struct
	if err := json.Unmarshal(b, &cfg); err != nil {
		t.Fatal(err)
	}
	breakglass := cfg.PackageConfig["github.com/gokrazy/breakglass"]
	keys := breakglass.ExtraFilePaths["/etc/breakglass.authorized_keys"]
	if want := "breakglass.authorized_keys"; keys != want {
		t.Errorf("ExtraFilePaths[\"/etc/breakglass.authorized_keys\"] = %s, want %s", keys, want)
	}

	// verify overwrite works (i.e. locates extrafiles)
	c = gok.Context{
		Args: []string{
			"--parent_dir", "packaging/gokrazy",
			"-i", "evcc",
			"overwrite",
			"--root=root.squashfs",
		},
	}
	t.Logf("running %q", append([]string{"<gok>"}, c.Args...))
	if err := c.Execute(context.Background()); err != nil {
		t.Fatalf("%v: %v", c.Args, err)
	}

}
