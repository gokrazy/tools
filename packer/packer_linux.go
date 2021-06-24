package packer

import "golang.org/x/sys/unix"

func rereadPartitions(fd uintptr) error {
	if _, _, errno := unix.Syscall(unix.SYS_IOCTL, fd, unix.BLKRRPART, 0); errno != 0 {
		return errno
	}
	return nil
}
