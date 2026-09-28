package gok

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path"
	"strings"

	"golang.org/x/sync/errgroup"
)

type resolvedModule struct {
	module  string
	version string
	goMod   []byte
}

// listModule resolves modulePath@version using the go command, which honors
// the full Go configuration (GOPROXY, GOPRIVATE, GONOPROXY, git credentials,
// settings made via go env -w, …).
//
// dir is the directory in which to run the go command. It should not be inside
// a Go module, so that the result does not depend on the current directory.
func listModule(ctx context.Context, dir, modulePath, version string) (*resolvedModule, error) {
	query := modulePath + "@" + version
	list := exec.CommandContext(ctx, "go", "list", "-m", "-json", query)
	list.Dir = dir
	cmdline := strings.Join(list.Args, " ")
	var stdout, stderr bytes.Buffer
	list.Stdout = &stdout
	list.Stderr = &stderr
	if err := list.Run(); err != nil {
		return nil, fmt.Errorf("%s: %v: %s", cmdline, err, strings.TrimSpace(stderr.String()))
	}
	var m struct {
		Path    string
		Version string
		GoMod   string // path to the .mod file in the module cache
	}
	if err := json.Unmarshal(stdout.Bytes(), &m); err != nil {
		return nil, fmt.Errorf("decoding %s output: %v", cmdline, err)
	}
	if m.Path != modulePath {
		return nil, fmt.Errorf("%s resolved to module %q, not %q", cmdline, m.Path, modulePath)
	}
	if m.Version == "" || m.GoMod == "" {
		return nil, fmt.Errorf("%s: no version or go.mod in output", cmdline)
	}
	goMod, err := os.ReadFile(m.GoMod)
	if err != nil {
		return nil, err
	}
	return &resolvedModule{
		module:  m.Path,
		version: m.Version,
		goMod:   goMod,
	}, nil
}

// resolveModule resolves importPath to the Go module containing it: the module
// path is the longest prefix of importPath for which the go command finds a
// module.
func resolveModule(ctx context.Context, importPath, version string) (*resolvedModule, error) {
	// Run outside of any module (and of the current directory’s go.mod).
	dir, err := os.MkdirTemp("", "gok-resolve-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(dir)

	parts := strings.Split(path.Clean(importPath), "/")
	resolved := make([]*resolvedModule, len(parts))
	errs := make([]error, len(parts))
	var eg errgroup.Group
	for idx := len(parts); idx > 0; idx-- {
		idx := idx // copy
		importPath := strings.Join(parts[:idx], "/")
		eg.Go(func() error {
			if importPath == "github.com" {
				// Short-circuit: github.com is not a Go module :)
				return nil
			}
			if strings.HasPrefix(importPath, "github.com/") &&
				!strings.ContainsRune(strings.TrimPrefix(importPath, "github.com/"), '/') {
				// Short-circuit: github.com/<something> references an
				// organisation or user, not a repository.
				return nil
			}
			// Most prefixes of an import path are not modules, so a failure
			// here is expected. It is only reported if no prefix resolves.
			resolved[idx-1], errs[idx-1] = listModule(ctx, dir, importPath, version)
			return nil
		})
	}
	eg.Wait() // never returns an error, see above

	for idx := len(parts); idx > 0; idx-- {
		if m := resolved[idx-1]; m != nil {
			return m, nil
		}
	}

	if err := ctx.Err(); err != nil {
		return nil, err
	}
	// The error for the full import path is the most informative one (e.g.
	// authentication failures for a private repository).
	if err := errs[len(parts)-1]; err != nil {
		return nil, fmt.Errorf("could not resolve import path %q to any Go module: %w", importPath, err)
	}
	return nil, fmt.Errorf("could not resolve import path %q to any Go module", importPath)
}
