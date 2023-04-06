package gok

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path"
	"strings"

	gokversion "github.com/gokrazy/tools/internal/version"
	"golang.org/x/mod/module"
	"golang.org/x/sync/errgroup"
)

type resolvedModule struct {
	module  string
	version string
	goMod   []byte
}

type latestResp struct {
	Version string `json:"Version"`
}

func proxyRequest(importPath, suffix string) (*http.Request, error) {
	proxyBase := "https://proxy.golang.org"
	if gp := os.Getenv("GOPROXY"); gp != "" {
		if strings.ContainsRune(gp, ',') ||
			strings.ContainsRune(gp, '|') ||
			gp == "off" ||
			gp == "direct" {
			return nil, fmt.Errorf("only GOPROXY=<url> is supported by the gok tool, not %q", gp)
		}
		proxyBase = strings.TrimSuffix(gp, "/")
	}
	escapedSuffix, err := module.EscapeVersion(suffix)
	if err != nil {
		return nil, err
	}
	if escapedSuffix != "@latest" {
		escapedSuffix = "@v/" + escapedSuffix
	}
	escapedImportPath, err := module.EscapePath(importPath)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequest("GET", proxyBase+"/"+escapedImportPath+"/"+escapedSuffix, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "gokrazy gok "+gokversion.ReadBrief())
	return req, nil
}

func moduleInfo(ctx context.Context, importPath, version string) (*latestResp, error) {
	suffix := version + ".info"
	if version == "latest" {
		suffix = "@latest"
	}
	req, err := proxyRequest(importPath, suffix)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	resp, err := http.DefaultClient.Do(req.WithContext(ctx))
	if err != nil {
		return nil, err
	}
	defer func() {
		io.ReadAll(resp.Body)
		resp.Body.Close()
	}()
	if resp.StatusCode == http.StatusNotFound {
		return nil, nil
	}
	if got, want := resp.StatusCode, http.StatusOK; got != want {
		return nil, fmt.Errorf("unexpected HTTP status: got %v, want %v", resp.Status, want)
	}
	var latest latestResp
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("reading HTTP response: %v", err)
	}
	if err := json.Unmarshal(b, &latest); err != nil {
		return nil, fmt.Errorf("decoding /@latest response: %v", err)
	}
	return &latest, nil
}

func resolveGoMod(ctx context.Context, importPath string, latest *latestResp) (*resolvedModule, error) {
	req, err := proxyRequest(importPath, latest.Version+".mod")
	if err != nil {
		return nil, err
	}
	resp, err := http.DefaultClient.Do(req.WithContext(ctx))
	if err != nil {
		return nil, err
	}
	defer func() {
		io.ReadAll(resp.Body)
		resp.Body.Close()
	}()
	if got, want := resp.StatusCode, http.StatusOK; got != want {
		return nil, fmt.Errorf("unexpected HTTP status: got %v, want %v", resp.Status, want)
	}
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("reading HTTP response: %v", err)
	}
	return &resolvedModule{
		module:  importPath,
		version: latest.Version,
		goMod:   b,
	}, nil
}

func resolveModule(ctx context.Context, importPath, version string) (*resolvedModule, error) {
	eg, latestctx := errgroup.WithContext(ctx)

	parts := strings.Split(path.Clean(importPath), "/")
	resps := make([]*latestResp, len(parts))
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
			resp, err := moduleInfo(latestctx, importPath, version)
			if err != nil {
				return err
			}
			resps[idx-1] = resp
			return nil
		})
	}

	if err := eg.Wait(); err != nil {
		return nil, err
	}

	for idx := len(parts); idx > 0; idx-- {
		importPath := strings.Join(parts[:idx], "/")
		resp := resps[idx-1]
		if resp == nil {
			continue
		}
		return resolveGoMod(ctx, importPath, resp)
	}

	return nil, fmt.Errorf("could not resolve import path %q to any Go module", importPath)
}
