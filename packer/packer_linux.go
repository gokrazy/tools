package packer

import (
	"os"

	"golang.org/x/sys/unix"
)

func rereadPartitions(o *os.File) error {
	// Make Linux re-read the partition table. Sequence of system calls like in fdisk(8).
	unix.Sync()

	if _, _, errno := unix.Syscall(unix.SYS_IOCTL, uintptr(o.Fd()), unix.BLKRRPART, 0); errno != 0 {
		return errno
	}

	if err := o.Sync(); err != nil {
		return err
	}

	unix.Sync()

	return nil
}
