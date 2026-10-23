//go:build !darwin

package packer

import "os"

func syncFile(f *os.File) error {
	return f.Sync()
}
