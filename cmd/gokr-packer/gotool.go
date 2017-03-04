package main

import (
	"bytes"
	"flag"
	"fmt"
	"log"
	"os"
	"os/exec"
	"strings"
)

var env = goEnv()

func goEnv() []string {
	goarch := "arm64" // Raspberry Pi 3
	if e := os.Getenv("GOARCH"); e != "" {
		goarch = e
	}

	env := os.Environ()
	for idx, e := range env {
		if strings.HasPrefix(e, "CGO_ENABLED=") {
			env[idx] = "CGO_ENABLED=0"
		}
	}
	return append(env, fmt.Sprintf("GOARCH=%s", goarch))
}

func install() error {
	pkgs := append(gokrazyPkgs, flag.Args()...)
	if *initPkg != "" {
		pkgs = append(pkgs, *initPkg)
	}

	// run “go get” for incomplete packages (most likely just not present)
	cmd := exec.Command("go",
		append([]string{"list", "-e", "-f", "{{ .ImportPath }} {{ if .Incomplete }}error{{ else }}ok{{ end }}"}, pkgs...)...)
	cmd.Env = env
	cmd.Stderr = os.Stderr
	output, err := cmd.Output()
	if err != nil {
		return err
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
			return err
		}
	}

	cmd = exec.Command("go",
		append([]string{"install", "-tags", "gokrazy"}, pkgs...)...)
	cmd.Env = env
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

func mainPackages(paths []string) ([]string, error) {
	// Shell out to the go tool for path matching (handling “...”)
	var buf bytes.Buffer
	cmd := exec.Command("go", append([]string{"list", "-f", "{{ .Name }}/{{ .Target }}"}, paths...)...)
	cmd.Env = env
	cmd.Stdout = &buf
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return nil, err
	}

	lines := strings.Split(buf.String(), "\n")
	result := make([]string, 0, len(lines))
	for _, line := range lines {
		if !strings.HasPrefix(line, "main/") {
			continue
		}
		result = append(result, strings.TrimPrefix(line, "main/"))
	}
	return result, nil
}

func packageDir(pkg string) (string, error) {
	b, err := exec.Command("go", "list", "-f", "{{ .Dir }}", pkg).Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(b)), nil
}
