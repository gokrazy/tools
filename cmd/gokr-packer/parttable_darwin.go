package main

import (
	"unsafe"

	"golang.org/x/sys/unix"
)

const (
	// TODO: get these into golang.org/x/sys/unix
	DKIOCGETBLOCKCOUNT = 0x40086419 // e.g. 31116288
	DKIOCGETBLOCKSIZE  = 0x40046418 // e.g. 512
)

func deviceSize(fd uintptr) (uint64, error) {
	var (
		blocksize  uint32
		blockcount uint64
	)
	if _, _, errno := unix.Syscall(unix.SYS_IOCTL, fd, DKIOCGETBLOCKSIZE, uintptr(unsafe.Pointer(&blocksize))); errno != 0 {
		return 0, errno
	}
	if _, _, errno := unix.Syscall(unix.SYS_IOCTL, fd, DKIOCGETBLOCKCOUNT, uintptr(unsafe.Pointer(&blockcount))); errno != 0 {
		return 0, errno
	}

	return uint64(blocksize) * blockcount, nil
}
