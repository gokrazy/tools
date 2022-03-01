package packer

import (
	"os"

	"golang.org/x/sys/unix"
)

func rereadPartitions(o *os.File) error {
	if _, _, errno := unix.Syscall(unix.SYS_IOCTL, uintptr(o.Fd()), unix.BLKRRPART, 0); errno != 0 {
		return errno
	}

	if err := o.Sync(); err != nil {
		return err
	}

	return nil
}
