package main

import (
	"archive/zip"
	"io"
	"os"
	"path/filepath"
	"runtime"
)

func hostLocaltime(tmpdir string) (string, error) {
	hostLocaltime := "/etc/localtime"
	if _, err := os.Stat(hostLocaltime); err == nil {
		return hostLocaltime, nil
	}

	// Fallback to time zone “Factory” from Go’s copy of zoneinfo.zip
	r, err := zip.OpenReader(filepath.Join(runtime.GOROOT(), "lib", "time", "zoneinfo.zip"))
	if err != nil {
		if os.IsNotExist(err) {
			// Some Go installations are missing lib/zoneinfo.zip
			// (e.g. Debian’s).
			return "", nil
		}
		return "", err
	}
	defer r.Close()

	for _, f := range r.File {
		if f.Name != "Factory" {
			continue
		}
		hostLocaltime = filepath.Join(tmpdir, "Factory")
		out, err := os.Create(hostLocaltime)
		if err != nil {
			return "", err
		}
		defer out.Close()
		rc, err := f.Open()
		if err != nil {
			return "", err
		}
		defer rc.Close()
		if _, err := io.Copy(out, rc); err != nil {
			return "", err
		}
		rc.Close()
		if err := out.Close(); err != nil {
			return "", err
		}
		break
	}
	return hostLocaltime, nil
}
