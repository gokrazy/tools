package packer

import (
	"encoding/base64"
	"errors"
	"fmt"
	"hash/fnv"
	"io"
	"os"
	"sort"
	"strings"

	"golang.org/x/mod/zip"
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
	checked, _ := zip.CheckDir(dir)
	checked.SizeError = nil // ignore maximum module size of 500 MB
	if err := checked.Err(); err != nil {
		return "", fmt.Errorf("CheckDir(%s): %v", dir, err)
	}

	h, err := quickHash(checked.Valid, func(name string) (io.ReadCloser, error) {
		return os.Open(name)
	})
	if err != nil {
		return "", err
	}
	return h, nil
}
