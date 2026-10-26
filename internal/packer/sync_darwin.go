package packer

import (
	"errors"
	"log"
	"os"

	"golang.org/x/sys/unix"
)

const (
	// On macOS, sys/disk.h declares DKIOCSYNCHRONIZECACHE.
	// macOS disk drivers like IOMediaBSDClient handle this ioctl
	// by flushing the device write cache.
	DKIOCSYNCHRONIZECACHE = 0x20006416 // _IO('d', 22)
)

func syncFile(f *os.File) error {
	// Go’s f.Sync is fcntl(F_FULLFSYNC) on macOS,
	// which works for files, but not device nodes (like /dev/diskN),
	// where macOS returns ENOTTY.
	err := f.Sync()
	if !errors.Is(err, unix.ENOTTY) {
		return err
	}
	// Encountered ENOTTY, fall back to fsync(2)
	// to write all buffered data to the driver (e.g. SD card driver).
	if err := unix.Fsync(int(f.Fd())); err != nil {
		return err
	}
	// Flush the device’s cache as well,
	// which may or may not have any effect,
	// depending on the device, e.g. USB card readers.
	// This is what macOS does when ejecting the device.
	if _, _, errno := unix.Syscall(unix.SYS_IOCTL, f.Fd(), DKIOCSYNCHRONIZECACHE, 0); errno != 0 {
		log.Printf("flushing write cache of %s: %v", f.Name(), errno)
	}
	return nil
}
