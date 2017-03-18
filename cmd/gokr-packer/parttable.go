package main

import (
	"encoding/binary"
	"fmt"
	"io"
	"log"
	"os"

	"golang.org/x/sys/unix"
)

var (
	active   = byte(0x80)
	inactive = byte(0x00)

	// invalidCHS results in using the sector values instead
	invalidCHS = [3]byte{0xFE, 0xFF, 0xFF}

	FAT   = byte(0xc)
	Linux = byte(0x83)

	signature = uint16(0xAA55)
)

func writePartitionTable(w io.Writer, devsize uint64) error {
	for _, v := range []interface{}{
		[446]byte{}, // boot code

		// partition 1
		active,
		invalidCHS,
		FAT,
		invalidCHS,
		uint32(8192),           // start at 8192 sectors
		uint32(100 * MB / 512), // 100MB in size

		// partition 2
		inactive,
		invalidCHS,
		FAT,
		invalidCHS,
		uint32(8192 + (100 * MB / 512)), // start after partition 1
		uint32(500 * MB / 512),          // 500MB in size

		// partition 3
		inactive,
		invalidCHS,
		FAT,
		invalidCHS,
		uint32(8192 + (600 * MB / 512)), // start after partition 2
		uint32(500 * MB / 512),          // 500MB in size

		// partition 3
		inactive,
		invalidCHS,
		Linux,
		invalidCHS,
		uint32(8192 + (1100 * MB / 512)),                   // start after partition 3
		uint32((devsize / 512) - 8192 - (1100 * MB / 512)), // remainder

		signature,
	} {
		if err := binary.Write(w, binary.LittleEndian, v); err != nil {
			return err
		}
	}

	return nil
}

func partition(path string) error {
	o, err := os.Create(path)
	if err != nil {
		return err
	}
	defer o.Close()

	devsize, err := deviceSize(uintptr(o.Fd()))
	if err != nil {
		return err
	}
	log.Printf("device holds %d bytes", devsize)

	if err := writePartitionTable(o, devsize); err != nil {
		return err
	}

	// Make Linux re-read the partition table. Sequence of system calls like in fdisk(8).
	unix.Sync()

	if _, _, errno := unix.Syscall(unix.SYS_IOCTL, uintptr(o.Fd()), unix.BLKRRPART, 0); errno != 0 {
		return fmt.Errorf("re-read partition table: %v", errno)
	}

	if err := o.Sync(); err != nil {
		return err
	}

	if err := o.Close(); err != nil {
		return err
	}

	unix.Sync()
	return nil
}
