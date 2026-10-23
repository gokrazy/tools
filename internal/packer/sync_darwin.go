package packer

import (
	"os"

	"golang.org/x/sys/unix"
)

// syncFile is like f.Sync, but also works for devices such as SD cards: on
// darwin, f.Sync uses F_FULLFSYNC, which devices reject (e.g. with ENOTTY), so
// we fall back to a plain fsync.
func syncFile(f *os.File) error {
	if err := f.Sync(); err != nil {
		return unix.Fsync(int(f.Fd()))
	}
	return nil
}
