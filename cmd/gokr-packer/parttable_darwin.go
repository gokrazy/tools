package main

import (
	"fmt"
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

func rereadPartitions(fd uintptr) error {
	return fmt.Errorf("gokrazy is currently missing code for re-reading partition tables on your operating system. Please see the README at https://github.com/gokrazy/tools for alternatives, and consider contributing code to fix this")
}
