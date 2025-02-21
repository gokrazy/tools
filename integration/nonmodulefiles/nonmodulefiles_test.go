package nonmodulefiles_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/gokrazy/tools/gok"
)

// TestNonModuleFiles adds a package to a gokrazy instance which uses a replace
// directive and points to a directory with files that cannot be shipped as a Go
// module.
func TestNonModuleFiles(t *testing.T) {
	// Run this whole test in a throw-away temporary directory to not litter the
	// gokrazy/tools repository working copy.
	parent := t.TempDir()

	// create a new instance
	c := gok.Context{
		Args: []string{
			"--parent_dir=" + parent,
			"-i", "nonmod",
			"new",
			// TODO: the --empty flag seems to have no effect. we need no
			// packages in this instance, other than the failing one.
			"--empty",
		},
	}
	t.Logf("running %q", append([]string{"<gok>"}, c.Args...))
	if err := c.Execute(context.Background()); err != nil {
		t.Fatalf("%v: %v", c.Args, err)
	}

	// create a local package and add it to the instance
	wd := t.TempDir()
	if err := os.WriteFile(filepath.Join(wd, "go.mod"), []byte("module some/program"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(wd, "hello.go"), []byte("package main\nfunc main() {}"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(wd, "1°-steps.txt"), nil, 0644); err != nil {
		t.Fatal(err)
	}

	c = gok.Context{
		Args: []string{
			"--parent_dir=" + parent,
			"-i", "nonmod",
			"add",
			wd,
		},
	}
	t.Logf("running %q", append([]string{"<gok>"}, c.Args...))
	if err := c.Execute(context.Background()); err != nil {
		t.Fatalf("%v: %v", c.Args, err)
	}

	// verify overwrite works, i.e. does not choke on file names that cannot go
	// into a Go module zip file
	c = gok.Context{
		Args: []string{
			"--parent_dir=" + parent,
			"-i", "nonmod",
			"overwrite",
			"--root=root.squashfs",
		},
	}
	t.Logf("running %q", append([]string{"<gok>"}, c.Args...))
	if err := c.Execute(context.Background()); err != nil {
		t.Fatalf("%v: %v", c.Args, err)
	}

}
