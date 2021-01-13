package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"golang.org/x/sync/errgroup"
)

var env = goEnv()

func goEnv() []string {
	goarch := "arm64" // Raspberry Pi 3
	if e := os.Getenv("GOARCH"); e != "" {
		goarch = e
	}

	goos := "linux" // Raspberry Pi 3
	if e := os.Getenv("GOOS"); e != "" {
		goos = e
	}

	env := os.Environ()
	for idx, e := range env {
		if strings.HasPrefix(e, "CGO_ENABLED=") {
			env[idx] = "CGO_ENABLED=0"
		}
		if strings.HasPrefix(e, "GOBIN=") {
			env[idx] = "GOBIN="
		}
	}
	return append(env,
		fmt.Sprintf("GOARCH=%s", goarch),
		fmt.Sprintf("GOOS=%s", goos),
		"CGO_ENABLED=0",
		"GOBIN=")
}

func build(bindir string, packageBuildFlags map[string][]string) error {
	pkgs := append(gokrazyPkgs, flag.Args()...)
	if *initPkg != "" {
		pkgs = append(pkgs, *initPkg)
	} else {
		// The default init template requires github.com/gokrazy/gokrazy:
		pkgs = append(pkgs, "github.com/gokrazy/gokrazy")
	}

	incompletePkgs := append(pkgs, *kernelPackage, *firmwarePackage)

	// run “go get” for incomplete packages (most likely just not present)
	cmd := exec.Command("go",
		append([]string{"list", "-e", "-f", "{{ .ImportPath }} {{ if .Incomplete }}error{{ else }}ok{{ end }}"}, incompletePkgs...)...)
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
			append([]string{"get"}, incomplete...)...)
		cmd.Env = env
		cmd.Stderr = os.Stderr
		if err := cmd.Run(); err != nil {
			return fmt.Errorf("%v: %v", cmd.Args, err)
		}
	}

	mainPkgs, err := mainPackages(pkgs)
	if err != nil {
		return err
	}
	var eg errgroup.Group
	for _, pkg := range mainPkgs {
		pkg := pkg // copy
		eg.Go(func() error {
			args := []string{
				"build",
				"-tags", "gokrazy",
				"-o", filepath.Join(bindir, filepath.Base(pkg.Target)),
			}
			if buildFlags := packageBuildFlags[pkg.ImportPath]; len(buildFlags) > 0 {
				args = append(args, buildFlags...)
			}
			args = append(args, pkg.ImportPath)
			fmt.Println("go", args)
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

func mainPackages(paths []string) ([]Pkg, error) {
	// Shell out to the go tool for path matching (handling “...”)
	var buf bytes.Buffer
	cmd := exec.Command("go", append([]string{"list", "-json"}, paths...)...)
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

func packageDir(pkg string) (string, error) {
	cmd := exec.Command("go", "list", "-f", "{{ .Dir }}", pkg)
	cmd.Stderr = os.Stderr
	b, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("%v: %v", cmd.Args, err)
	}
	return strings.TrimSpace(string(b)), nil
}
