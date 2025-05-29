package gok

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/gokrazy/internal/config"
	"github.com/gokrazy/internal/instanceflag"
	"github.com/spf13/cobra"
	"golang.org/x/mod/modfile"
)

// addCmd is gok add.
var addCmd = &cobra.Command{
	GroupID:               "edit",
	Use:                   "add [flags] importpath[@version]",
	DisableFlagsInUseLine: true,
	Short:                 "Add a Go package to a gokrazy instance",
	Long: `Add a Go package to a gokrazy instance.

This command creates the required build directory, runs go get, and adds
the specified package to the gokrazy instance configuration (Packages field).

When using a relative or absolute path, it configures a replace directive:
https://go.dev/ref/mod#go-mod-file-replace

Examples:
  # Add a Go package from the internet:
  % gok -i scan2drive add github.com/gokrazy/rsync/cmd/gokr-rsyncd

  # …same, but using a specific version:
  % gok -i scan2drive add github.com/gokrazy/rsync/cmd/gokr-rsyncd@v2

  # Add a Go package from local disk (using a replace directive):
  % gok -i scan2drive add /home/michael/projects/scanui/cmd/scanui

`,
	RunE: func(cmd *cobra.Command, args []string) error {
		if cmd.Flags().NArg() != 1 {
			fmt.Fprint(os.Stderr, `expected Go package name, name@version, or path

`)
			return cmd.Usage()
		}

		return addImpl.run(cmd.Context(), args[0], cmd.OutOrStdout(), cmd.OutOrStderr())
	},
}

type addImplConfig struct{}

var addImpl addImplConfig

func init() {
	instanceflag.RegisterPflags(addCmd.Flags())
}

type packageInfo struct {
	// Dir is the directory on the local disk containing the package sources,
	// e.g. /home/michael/projects/stapelberg/localmod/cmd/sup.
	Dir string

	// ImportPath is the Go import path of the package,
	// e.g. stapelberg/localmod/cmd/sup.
	ImportPath string

	// Name is the Go package name, which should be main (for Go binaries).
	Name string // TODO: should we warn about non-"main" packages?

	Module struct {
		// Path is the module path, e.g. stapelberg/localmod
		Path string

		// Dir is the directory on the local disk containing the module,
		// e.g. /home/michael/projects/stapelberg/localmod.
		Dir string
	}
}

func inspectDir(ctx context.Context, abs string) (*packageInfo, error) {
	listPackage := exec.CommandContext(ctx, "go", "list", "-json")
	listPackage.Dir = abs
	listPackage.Stderr = os.Stderr
	output, err := listPackage.Output()
	if err != nil {
		return nil, fmt.Errorf("%v: %v", listPackage.Args, err)
	}
	var info packageInfo
	if err := json.Unmarshal(output, &info); err != nil {
		return nil, err
	}
	return &info, nil
}

func (r *addImplConfig) createGoMod(ctx context.Context, buildDir, path string, stdout, stderr io.Writer) error {
	modInit := exec.CommandContext(ctx, "go", "mod", "init", "gokrazy/build/"+path)
	modInit.Dir = buildDir
	modInit.Stderr = stderr
	if err := modInit.Run(); err != nil {
		return fmt.Errorf("%v: %v", modInit.Args, err)
	}
	return nil
}

func (r *addImplConfig) copyReplaceDirectives(ctx context.Context, oldDir, newDir string, stdout, stderr io.Writer) error {
	oldGoMod, err := os.ReadFile(filepath.Join(oldDir, "go.mod"))
	if err != nil {
		return fmt.Errorf("old go.mod does not exist: %v", err)
	}
	newPath := filepath.Join(newDir, "go.mod")
	newGoMod, err := os.ReadFile(newPath)
	if err != nil {
		return fmt.Errorf("new go.mod does not exist: %v", err)
	}
	oldf, err := modfile.Parse("go.mod", oldGoMod, nil)
	if err != nil {
		return fmt.Errorf("parsing old go.mod: %v", err)
	}
	newf, err := modfile.Parse("go.mod", newGoMod, nil)
	if err != nil {
		return fmt.Errorf("parsing new go.mod: %v", err)
	}

	for _, r := range oldf.Replace {
		if err := newf.AddReplace(r.Old.Path, r.Old.Version, r.New.Path, r.New.Version); err != nil {
			return err
		}
	}

	b, err := newf.Format()
	if err != nil {
		return err
	}

	if err := replaceFile(newPath, b, 0600); err != nil {
		return err
	}
	return nil
}

func (r *addImplConfig) addLocal(ctx context.Context, abs string, stdout, stderr io.Writer) error {
	pkg, err := inspectDir(ctx, abs)
	if err != nil {
		return err
	}
	log.Printf(`Adding the following package to gokrazy instance %q:
  Go package  : %s
  in Go module: %s
  in local dir: %s`, instanceflag.Instance(), pkg.ImportPath, pkg.Module.Path, pkg.Dir)

	buildDir := filepath.Join(config.InstancePath(), "builddir", pkg.ImportPath)
	if _, err := os.Stat(buildDir); err != nil {
		log.Printf("Creating gokrazy builddir for package %s", pkg.ImportPath)
		if err := os.MkdirAll(buildDir, 0755); err != nil {
			return fmt.Errorf("could not create builddir: %v", err)
		}
	}

	// create go.mod with gokrazy/build/<module-path>
	if _, err := os.Stat(filepath.Join(buildDir, "go.mod")); err == nil {
		log.Printf("Adding replace directive to existing go.mod")
	} else {
		log.Printf("Creating go.mod with replace directive")
		if err := r.createGoMod(ctx, buildDir, pkg.Module.Path, stdout, stderr); err != nil {
			return err
		}
	}
	modEdit := exec.CommandContext(ctx, "go", "mod", "edit", "-replace", pkg.Module.Path+"="+pkg.Module.Dir, "go.mod")
	modEdit.Dir = buildDir
	modEdit.Stderr = os.Stderr
	if err := modEdit.Run(); err != nil {
		return fmt.Errorf("%v: %v", modEdit.Args, err)
	}

	if err := r.copyReplaceDirectives(ctx, pkg.Module.Dir, buildDir, stdout, stderr); err != nil {
		return err
	}

	// Add a require line to go.mod. We use go mod edit instead of go get
	// because the latter does not work for evcc: “panic: internal error: can't
	// find reason for requirement on github.com/rogpeppe/go-internal@v1.6.1.”
	const zeroVersion = "v0.0.0-00010101000000-000000000000"
	get := exec.CommandContext(ctx, "go", "mod", "edit", "-require", pkg.Module.Path+"@"+zeroVersion)
	get.Dir = buildDir
	get.Stderr = os.Stderr
	if err := get.Run(); err != nil {
		return fmt.Errorf("%v: %v", get.Args, err)
	}

	if err := r.addPackageToConfig(pkg.ImportPath); err != nil {
		return err
	}

	log.Printf("All done! Next, use 'gok overwrite' (first deployment), 'gok update' (following deployments) or 'gok run' (run on running instance temporarily)")

	return nil
}

func (r *addImplConfig) addPackageToConfig(importPath string) error {
	cfg, err := config.ApplyInstanceFlag()
	if err != nil {
		return err
	}
	for _, existing := range cfg.Packages {
		if existing == importPath {
			log.Printf("Package already configured (see 'gok -i %s edit')", instanceflag.Instance())
			return nil
		}
	}
	log.Printf("Adding package to gokrazy config")
	cfg.Packages = append(cfg.Packages, importPath)
	b, err := cfg.FormatForFile()
	if err != nil {
		return err
	}
	if err := replaceFile(config.InstanceConfigPath(), b, 0600); err != nil {
		return fmt.Errorf("updating config.json: %v", err)
	}
	return nil
}

func (r *addImplConfig) addNonLocal(ctx context.Context, arg string, stdout, stderr io.Writer) error {
	log.Printf("Adding %s as a (non-local) package to gokrazy instance %s", arg, instanceflag.Instance())
	importPath := arg
	version := "latest"
	if idx := strings.IndexByte(importPath, '@'); idx > -1 {
		// Trim @version suffix from import path, if any
		version = importPath[idx+1:]
		importPath = importPath[:idx]
	}
	resolved, err := resolveModule(ctx, importPath, version)
	if err != nil {
		return err
	}
	log.Printf(`Adding the following package to gokrazy instance %q:
  Go package  : %s
  in Go module: %s`, instanceflag.Instance(), importPath, resolved.module)

	buildDir := filepath.Join(config.InstancePath(), "builddir", resolved.module)
	if _, err := os.Stat(buildDir); err != nil {
		log.Printf("Creating gokrazy builddir for module %s", resolved.module)
		if err := os.MkdirAll(buildDir, 0755); err != nil {
			return fmt.Errorf("could not create builddir: %v", err)
		}
	}

	if _, err := os.Stat(filepath.Join(buildDir, "go.mod")); err == nil {
		log.Printf("Adding require line to existing go.mod")
	} else {
		log.Printf("Creating go.mod based on upstream go.mod")
		modf, err := modfile.Parse("go.mod", resolved.goMod, nil)
		if err != nil {
			return fmt.Errorf("parsing old go.mod: %v", err)
		}
		if err := modf.AddModuleStmt("gokrazy/build/" + resolved.module); err != nil {
			return err
		}

		b, err := modf.Format()
		if err != nil {
			return err
		}

		if err := os.WriteFile(filepath.Join(buildDir, "go.mod"), b, 0600); err != nil {
			return err
		}
	}

	// Add a require line to go.mod. We use go mod edit instead of go get
	// because the latter does not work for evcc: “panic: internal error: can't
	// find reason for requirement on github.com/rogpeppe/go-internal@v1.6.1.”
	get := exec.CommandContext(ctx, "go", "mod", "edit", "-require", resolved.module+"@"+resolved.version)
	get.Dir = buildDir
	get.Stderr = os.Stderr
	if err := get.Run(); err != nil {
		return fmt.Errorf("%v: %v", get.Args, err)
	}

	if err := r.addPackageToConfig(importPath); err != nil {
		return err
	}

	return nil
}

func (r *addImplConfig) run(ctx context.Context, arg string, stdout, stderr io.Writer) error {
	parentDir := instanceflag.ParentDir()
	instance := instanceflag.Instance()

	if _, err := os.Stat(filepath.Join(parentDir, instance)); err != nil {
		return fmt.Errorf("instance %q does not exist (%v), create it using 'gok -i %s new'", instance, err, instance)
	}

	// Clear cases: an absolute path on the local disk
	// (e.g. /home/michael/go/src/mytool), or an explicitly relative path
	// (./mytool or ../mytool).
	isPath := strings.HasPrefix(arg, string(os.PathSeparator)) ||
		strings.HasPrefix(arg, ".")
	if !isPath {
		// We are less sure now. The argument could still be a relative path
		// (mytool), so see if the directory exists
		if _, err := os.Stat(arg); err == nil {
			isPath = true
		}
	}

	if isPath {
		abs, err := filepath.Abs(arg)
		if err != nil {
			return err
		}
		return r.addLocal(ctx, abs, stdout, stderr)
	}

	return r.addNonLocal(ctx, arg, stdout, stderr)
}
