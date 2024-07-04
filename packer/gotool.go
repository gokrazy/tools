package packer

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"github.com/gokrazy/tools/internal/measure"
	"golang.org/x/mod/modfile"
	"golang.org/x/sync/errgroup"
)

const logExec = false

func DefaultTags() []string {
	return []string{
		"gokrazy",
		"netgo",
		"osusergo",
	}
}

func TargetArch() string {
	if arch := os.Getenv("GOARCH"); arch != "" {
		return arch
	}
	return "arm64" // Raspberry Pi 3, 4, Zero 2 W
}

var (
	envOnce sync.Once
	env     []string
)

func goEnv() []string {
	goarch := TargetArch()

	goos := "linux" // Raspberry Pi 3, 4, Zero 2 W
	if e := os.Getenv("GOOS"); e != "" {
		goos = e
	}

	cgoEnabledFound := false
	env := os.Environ()
	for idx, e := range env {
		if strings.HasPrefix(e, "CGO_ENABLED=") {
			cgoEnabledFound = true
		}
		if strings.HasPrefix(e, "GOBIN=") {
			env[idx] = "GOBIN="
		}
	}
	if !cgoEnabledFound {
		env = append(env, "CGO_ENABLED=0")
	}
	return append(env,
		fmt.Sprintf("GOARCH=%s", goarch),
		fmt.Sprintf("GOOS=%s", goos),
		"GOBIN=")
}

func Env() []string {
	envOnce.Do(func() {
		env = goEnv()
	})
	return env
}

func InitDeps(initPkg string) []string {
	if initPkg != "" {
		return []string{initPkg}
	}
	// The default init template requires github.com/gokrazy/gokrazy:
	return []string{"github.com/gokrazy/gokrazy"}
}

func BuildDir(importPath string) string {
	importPath = strings.TrimSuffix(importPath, "/...")
	buildDir := filepath.Join("builddir", importPath)

	// Search for go.mod from most specific to least specific directory,
	// e.g. starting at builddir/github.com/gokrazy/gokrazy/cmd/dhcp and ending
	// at builddir/. This allows the user to specify the granularity of the
	// builddir tree:
	//
	// - a finely-grained per-package builddir
	// - a per-module builddir (convenient when working with replace directives)
	// - a per-org builddir (convenient for wide-reaching replace directives)
	// - a single builddir, preserving behavior of older gokrazy
	parts := strings.Split(buildDir, string(os.PathSeparator))
	for idx := len(parts); idx > 0; idx-- {
		dir := strings.Join(parts[:idx], string(os.PathSeparator))
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
	}
	return buildDir
}

func BuildDirOrMigrate(importPath string) (string, error) {
	buildDir := BuildDir(importPath)

	// Create and bootstrap a per-package builddir/ by copying go.mod
	// from the root if there is no go.mod in the builddir yet.
	if err := os.MkdirAll(buildDir, 0755); err != nil {
		return "", err
	}
	goMod := filepath.Join(buildDir, "go.mod")
	goSum := filepath.Join(buildDir, "go.sum")
	if _, err := os.Stat(goMod); os.IsNotExist(err) {
		rootGoMod, err := os.ReadFile("go.mod")
		if err != nil && !os.IsNotExist(err) {
			return "", err
		}
		migrating := err == nil // root go.mod exists

		wd, err := os.Getwd()
		if err != nil {
			return "", err
		}

		// We need to set a synthetic module path for the go.mod files within
		// builddir/ to cover the case where one is building a gokrazy image
		// from within the working directory of one of the modules
		// (e.g. building from a working copy of github.com/rtr7/router7). go
		// get does not work in that situation if the module is named
		// e.g. github.com/rtr7/router7, so name it gokrazy/build/router7
		// instead.
		modulePath := "gokrazy/build/" + filepath.Base(wd)

		if os.IsNotExist(err) {
			rootGoMod = []byte(fmt.Sprintf("module %s\n", modulePath))
		}

		f, err := modfile.Parse("go.mod", rootGoMod, nil)
		if err != nil {
			return "", err
		}
		f.AddModuleStmt(modulePath)
		for _, replace := range f.Replace {
			oldPath := replace.Old.Path
			oldVersion := replace.Old.Version
			fixedPath := replace.New.Path
			if replace.New.Version == "" {
				// Turn relative replace paths in the root go.mod file into absolute
				// ones to keep them working within the builddir/.
				if !filepath.IsAbs(fixedPath) {
					fixedPath = filepath.Join(wd, replace.New.Path)
				}
			}
			newVersion := replace.New.Version
			if err := f.DropReplace(oldPath, oldVersion); err != nil {
				return "", err
			}
			if err := f.AddReplace(oldPath, oldVersion, fixedPath, newVersion); err != nil {
				return "", err
			}
		}
		b, err := f.Format()
		if err != nil {
			return "", err
		}

		if err := os.WriteFile(goMod, b, 0644); err != nil {
			return "", err
		}

		if migrating {
			log.Printf("Migrated go.mod to %s, see https://gokrazy.org/development/modules/", goMod)
		}

		rootGoSum, err := os.ReadFile("go.sum")
		if err != nil && !os.IsNotExist(err) {
			return "", err
		}
		if err := os.WriteFile(goSum, rootGoSum, 0644); err != nil {
			return "", err
		}
	}
	return buildDir, nil
}

func warnWithoutProxy() {
	goproxy := exec.Command("go", "env", "GOPROXY")
	goproxy.Env = Env()
	goproxy.Stderr = os.Stderr
	if logExec {
		log.Printf("getIncomplete: %v", goproxy.Args)
	}
	out, err := goproxy.Output()
	if err != nil {
		log.Printf("%v: %v", goproxy.Args, err)
	}
	if strings.TrimSpace(string(out)) != "direct" {
		return
	}
	fmt.Println()
	log.Printf("WARNING: you’re using GOPROXY=direct, which means " +
		"the go tool needs to work with Git repositories, which is " +
		"inefficient and slow. Consider enabling the Go Proxy: " +
		"go env -w GOPROXY=https://proxy.golang.org,direct")
}

func getIncomplete(buildDir string, incomplete []string) error {
	warnWithoutProxy()

	log.Printf("getting incomplete packages %v", incomplete)
	cmd := exec.Command("go",
		append([]string{
			"get",
		}, incomplete...)...)
	cmd.Dir = buildDir
	cmd.Env = Env()
	cmd.Stderr = os.Stderr
	if logExec {
		log.Printf("getIncomplete: %v (in %s)", cmd.Args, buildDir)
	}
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("%v: %v", cmd.Args, err)
	}
	return nil
}

func getPkg(buildDir string, pkg string) error {
	// run “go get” for incomplete packages (most likely just not present)
	cmd := exec.Command("go",
		append([]string{
			"list",
			"-mod=mod",
			"-e",
			"-f", "{{ .ImportPath }} {{ if .Incomplete }}error{{ else }}ok{{ end }}",
		}, pkg)...)
	cmd.Env = Env()
	cmd.Dir = buildDir
	cmd.Stderr = os.Stderr
	if logExec {
		log.Printf("getPkg: %v (in %s)", cmd.Args, buildDir)
	}
	output, err := cmd.Output()
	if err != nil {
		// TODO: can we make this more specific? when starting with an empty
		// dir, getting github.com/gokrazy/gokrazy/cmd/dhcp does not work
		// otherwise

		// Treat any error as incomplete
		return getIncomplete(buildDir, []string{pkg})
		// return fmt.Errorf("%v: %v", cmd.Args, err)
	}
	if strings.TrimSpace(string(output)) == "" {
		// If our package argument matches no packages
		// (e.g. github.com/rtr7/router7/cmd/... without having the
		// github.com/rtr7/router7 module in go.mod), the output will be empty,
		// and we should try getting the corresponding package/module.
		return getIncomplete(buildDir, []string{pkg})
	}
	var incomplete []string
	const errorSuffix = " error"
	for _, line := range strings.Split(string(output), "\n") {
		if !strings.HasSuffix(line, errorSuffix) {
			continue
		}
		incomplete = append(incomplete, strings.TrimSuffix(line, errorSuffix))
	}

	if len(incomplete) > 0 {
		return getIncomplete(buildDir, incomplete)
	}
	return nil
}

type BuildEnv struct {
	BuildDir func(string) (string, error)
}

func (be *BuildEnv) Build(bindir string, packages []string, packageBuildFlags, packageBuildTags map[string][]string, noBuildPackages []string) error {
	done := measure.Interactively("building (go compiler)")
	defer done("")

	var eg errgroup.Group
	for _, incompleteNoBuildPkg := range noBuildPackages {
		buildDir, err := be.BuildDir(incompleteNoBuildPkg)
		if err != nil {
			return fmt.Errorf("buildDir(%s): %v", incompleteNoBuildPkg, err)
		}

		if err := getPkg(buildDir, incompleteNoBuildPkg); err != nil {
			return err
		}
	}
	for _, incompletePkg := range packages {
		buildDir, err := be.BuildDir(incompletePkg)
		if err != nil {
			return fmt.Errorf("buildDir(%s): %v", incompletePkg, err)
		}

		if err := getPkg(buildDir, incompletePkg); err != nil {
			return err
		}

		mainPkgs, err := be.MainPackages([]string{incompletePkg})
		if err != nil {
			return err
		}
		for _, pkg := range mainPkgs {
			pkg := pkg // copy
			eg.Go(func() error {
				args := []string{
					"build",
					"-mod=mod",
					"-o", filepath.Join(bindir, filepath.Base(pkg.Target)),
				}
				tags := append(DefaultTags(), packageBuildTags[pkg.ImportPath]...)
				args = append(args, "-tags="+strings.Join(tags, ","))
				if buildFlags := packageBuildFlags[pkg.ImportPath]; len(buildFlags) > 0 {
					args = append(args, buildFlags...)
				}
				args = append(args, pkg.ImportPath)
				cmd := exec.Command("go", args...)
				cmd.Env = Env()
				cmd.Dir = buildDir
				cmd.Stderr = os.Stderr
				if logExec {
					log.Printf("Build: %v (in %s)", cmd.Args, buildDir)
				}
				if err := cmd.Run(); err != nil {
					return fmt.Errorf("%v: %v", cmd.Args, err)
				}
				return nil
			})
		}
	}
	return eg.Wait()
}

type Pkg struct {
	Name       string `json:"Name"`
	ImportPath string `json:"ImportPath"`
	Target     string `json:"Target"`
}

func (p *Pkg) Basename() string {
	return filepath.Base(p.Target)
}

func (be *BuildEnv) mainPackage(pkg string) ([]Pkg, error) {
	buildDir, err := be.BuildDir(pkg)
	if err != nil {
		return nil, fmt.Errorf("BuildDir(%s): %v", pkg, err)
	}

	var buf bytes.Buffer
	cmd := exec.Command("go", append([]string{"list", "-tags", "gokrazy", "-json"}, pkg)...)
	cmd.Dir = buildDir
	cmd.Env = Env()
	cmd.Stdout = &buf
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("%v: %v", cmd.Args, err)
	}
	var result []Pkg
	dec := json.NewDecoder(&buf)
	for {
		var p Pkg
		if err := dec.Decode(&p); err == io.EOF {
			break
		} else if err != nil {
			return nil, err
		}
		if p.Name != "main" {
			continue
		}
		result = append(result, p)
	}
	return result, nil
}

func (be *BuildEnv) MainPackages(pkgs []string) ([]Pkg, error) {
	// Shell out to the go tool for path matching (handling “...”)
	var (
		eg       errgroup.Group
		resultMu sync.Mutex
		result   []Pkg
	)
	for _, pkg := range pkgs {
		pkg := pkg // copy
		eg.Go(func() error {
			p, err := be.mainPackage(pkg)
			if err != nil {
				return err
			}
			resultMu.Lock()
			defer resultMu.Unlock()
			result = append(result, p...)
			return nil
		})
	}
	if err := eg.Wait(); err != nil {
		return nil, err
	}
	sort.Slice(result, func(i, j int) bool {
		return result[i].Basename() < result[j].Basename()
	})
	return result, nil
}

func PackageDir(pkg string) (string, error) {
	buildDir, err := BuildDirOrMigrate(pkg)
	if err != nil {
		return "", fmt.Errorf("PackageDirs(%s): %v", pkg, err)
	}

	cmd := exec.Command("go", "list", "-mod=mod", "-tags", "gokrazy", "-f", "{{ .Dir }}", pkg)
	cmd.Env = Env()
	cmd.Dir = buildDir
	cmd.Stderr = os.Stderr
	if logExec {
		log.Printf("PackageDir: %v (in %s)", cmd.Args, buildDir)
	}
	b, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("%v: %v", cmd.Args, err)
	}
	return strings.TrimSpace(string(b)), nil
}

// PackageDirs returns the package directories, in the same order as the argument
func PackageDirs(pkgs []string) ([]string, error) {
	var eg errgroup.Group

	// pre-allocate the slice for concurrent writes
	dirs := make([]string, len(pkgs))
	for i, pkg := range pkgs {
		i := i     // copy
		pkg := pkg // copy
		eg.Go(func() error {
			dir, err := PackageDir(pkg)
			if err != nil {
				return err
			}
			dirs[i] = dir
			return nil
		})
	}
	if err := eg.Wait(); err != nil {
		return nil, err
	}
	return dirs, nil
}
