package gokadd_test

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/gokrazy/internal/config"
	"github.com/gokrazy/tools/gok"
)

// TestGokAdd tests `gok add`ing to a gokrazy instance.
func TestGokAdd(t *testing.T) {
	fakeDHCPPath, err := filepath.Abs("testdata/cmd/dhcp")
	if err != nil {
		t.Fatalf("filepath.Abs(testdata/cmd/dhcp): %v", err)
	}
	t.Chdir(t.TempDir()) // Avoid littering gokrazy/tools source/working tree.
	const instance = "gokadd_test"

	execute := func(args []string) {
		c := gok.Context{Args: append([]string{"--parent_dir", ".", "-i", instance}, args...)}
		t.Helper()
		fmt.Printf("running %q", append([]string{"<gok>"}, c.Args...))
		if err := c.Execute(context.Background()); err != nil {
			t.Fatalf("%v: %v", c.Args, err)
		}
	}

	mustRead := func(path string) []byte {
		t.Helper()
		b, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("os.ReadFile(%v): %v", path, err)
		}
		return b
	}

	readConfig := func() config.Struct {
		t.Helper()
		path := instance + "/config.json"
		b := mustRead(path)
		var cfg config.Struct
		if err := json.Unmarshal(b, &cfg); err != nil {
			t.Fatal(err)
		}
		return cfg
	}

	// Establish baseline: dhcp is part of default GokrazyPackages.
	execute([]string{"new"})
	initialConfig := readConfig()
	if !slices.Contains(initialConfig.GokrazyPackagesOrDefault(), "github.com/gokrazy/gokrazy/cmd/dhcp") {
		t.Fatalf("dhcp isn't part of default GokrazyPackages; test should be updated to rely on a package that is; config: %v", initialConfig)
	}

	// Assert that adding a non-default package ends up in Packages.
	execute([]string{"add", "github.com/stapelberg/scan2drive/cmd/scan2drive"})
	postScan2Drive := readConfig()
	if !slices.Contains(postScan2Drive.Packages, "github.com/stapelberg/scan2drive/cmd/scan2drive") {
		t.Fatalf("gok add scan2drive succeeded but scan2drive is not in Packages; config: %v", postScan2Drive)
	}

	// Assert that adding a default GokrazyPackages doesn't change the config.
	execute([]string{"add", "github.com/gokrazy/gokrazy/cmd/dhcp"})
	postDHCP := readConfig()
	if postDHCP.GokrazyPackages != nil {
		t.Fatalf("gok add dhcp resulted in GokrazyPackages being non-default; config: %v", postDHCP)
	}
	if slices.Contains(postDHCP.Packages, "github.com/gokrazy/gokrazy/cmd/dhcp") {
		t.Fatalf("gok add dhcp resulted in dhcp being added to Packages, even though it is a GokrazyPackages; config: %v", postDHCP)
	}

	// Assert that adding a default GokrazyPackages with a local override still works.
	execute([]string{"add", fakeDHCPPath})
	postCustomDHCP := readConfig()
	if postCustomDHCP.GokrazyPackages != nil {
		t.Fatalf("gok add %v resulted in GokrazyPackages being non-default; config: %v", fakeDHCPPath, postCustomDHCP)
	}
	if slices.Contains(postCustomDHCP.Packages, "github.com/gokrazy/gokrazy/cmd/dhcp") {
		t.Fatalf("gok add %v resulted in dhcp being added to Packages, even though it is a GokrazyPackages; config: %v", fakeDHCPPath, postCustomDHCP)
	}

	gm := string(mustRead("gokadd_test/builddir/github.com/gokrazy/gokrazy/cmd/dhcp/go.mod"))
	if !strings.Contains(gm, "replace github.com/gokrazy/gokrazy => ") {
		t.Fatalf("after adding a custom DHCP module, builddir doesn't have a replace statement; full go.mod: %v", gm)
	}
	if !strings.Contains(gm, "require github.com/gokrazy/gokrazy v0.0.0-00010101000000-000000000000") {
		t.Fatalf("after adding a custom DHCP module, builddir doesn't have a zero-version require statement; full go.mod: %v", gm)
	}
}
