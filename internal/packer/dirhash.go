package packer

import (
	"encoding/base64"
	"errors"
	"fmt"
	"hash/fnv"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"golang.org/x/mod/sumdb/dirhash"
)

func quickHash(files []string, open func(string) (io.ReadCloser, error)) (string, error) {
	h := fnv.New128()
	files = append([]string(nil), files...)
	sort.Strings(files)
	for _, file := range files {
		if strings.Contains(file, "\n") {
			return "", errors.New("dirhash: filenames with newlines are not supported")
		}
		r, err := open(file)
		if err != nil {
			return "", err
		}
		hf := fnv.New128()
		_, err = io.Copy(hf, r)
		r.Close()
		if err != nil {
			return "", err
		}
		fmt.Fprintf(h, "%x  %s\n", hf.Sum(nil), file)
	}
	return "qh:" + base64.StdEncoding.EncodeToString(h.Sum(nil)), nil
}

// hashDir is like dirhash.HashDir, but it filters hidden files and uses
// quickHash.
func hashDir(dir string) (string, error) {
	files, err := dirhash.DirFiles(dir, dir)
	if err != nil {
		return "", err
	}
	n := 0
	for _, f := range files {
		rel := strings.TrimPrefix(f, filepath.Clean(dir)+"/")
		if strings.HasPrefix(rel, ".") {
			continue
		}
		files[n] = f
		n++
	}
	files = files[:n]
	h, err := quickHash(files, func(name string) (io.ReadCloser, error) {
		return os.Open(name)
	})
	if err != nil {
		return "", err
	}
	return h, nil
}
