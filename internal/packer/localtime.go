package packer

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

	// Fallback to time zone “Factory” from Go’s copy of zoneinfo.zip.
	//
	// Unfortunately, we cannot directly use the time/tzdata package (an
	// embedded copy of the timezone database in the Go standard library),
	// because the standard library only provides code to *load* that copy, but
	// not to write the loadable copy to a file (or to generate a timezone
	// database file).
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
