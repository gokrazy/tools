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
	"strings"

	"github.com/gokrazy/tools/internal/measure"
	"golang.org/x/sync/errgroup"
)

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
	return "arm64" // Raspberry Pi 3
}

var env = goEnv()

func goEnv() []string {
	goarch := TargetArch()

	goos := "linux" // Raspberry Pi 3
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

func Env() []string { return env }

func InitDeps(initPkg string) []string {
	if initPkg != "" {
		return []string{initPkg}
	}
	// The default init template requires github.com/gokrazy/gokrazy:
	return []string{"github.com/gokrazy/gokrazy"}
}

func Build(bindir string, packages []string, packageBuildFlags, packageBuildTags map[string][]string, noBuildPackages []string) error {
	done := measure.Interactively("building (go compiler)")
	defer done("")

	incompletePkgs := make([]string, 0, len(packages)+len(noBuildPackages))
	incompletePkgs = append(incompletePkgs, packages...)
	incompletePkgs = append(incompletePkgs, noBuildPackages...)

	// run “go get” for incomplete packages (most likely just not present)
	cmd := exec.Command("go",
		append([]string{
			"list",
			"-mod=mod",
			"-e",
			"-f", "{{ .ImportPath }} {{ if .Incomplete }}error{{ else }}ok{{ end }}",
		}, incompletePkgs...)...)
	cmd.Env = env
	cmd.Stderr = os.Stderr
	output, err := cmd.Output()
	if err != nil {
		return fmt.Errorf("%v: %v", cmd.Args, err)
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
		log.Printf("getting incomplete packages %v", incomplete)
		cmd = exec.Command("go",
			append([]string{
				"get",
			}, incomplete...)...)
		cmd.Env = env
		cmd.Stderr = os.Stderr
		if err := cmd.Run(); err != nil {
			return fmt.Errorf("%v: %v", cmd.Args, err)
		}
	}

	mainPkgs, err := MainPackages(packages)
	if err != nil {
		return err
	}
	var eg errgroup.Group
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
			cmd.Env = env
			cmd.Stderr = os.Stderr
			if err := cmd.Run(); err != nil {
				return fmt.Errorf("%v: %v", cmd.Args, err)
			}
			return nil
		})
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

func MainPackages(paths []string) ([]Pkg, error) {
	// Shell out to the go tool for path matching (handling “...”)
	var buf bytes.Buffer
	cmd := exec.Command("go", append([]string{"list", "-tags", "gokrazy", "-json"}, paths...)...)
	cmd.Env = env
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

func PackageDir(pkg string) (string, error) {
	cmd := exec.Command("go", "list", "-mod=mod", "-tags", "gokrazy", "-f", "{{ .Dir }}", pkg)
	cmd.Stderr = os.Stderr
	b, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("%v: %v", cmd.Args, err)
	}
	return strings.TrimSpace(string(b)), nil
}

func PackageDirs(pkgs []string) ([]string, error) {
	cmd := exec.Command("go", append([]string{"list", "-mod=mod", "-tags", "gokrazy", "-f", "{{ .Dir }}"}, pkgs...)...)
	cmd.Stderr = os.Stderr
	b, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("%v: %v", cmd.Args, err)
	}
	return strings.Split(strings.TrimSpace(string(b)), "\n"), nil
}
