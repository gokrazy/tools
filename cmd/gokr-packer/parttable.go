package main

import (
	"encoding/binary"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"os/exec"
	"strconv"
	"syscall"

	"golang.org/x/sys/unix"
)

var (
	active   = byte(0x80)
	inactive = byte(0x00)

	// invalidCHS results in using the sector values instead
	invalidCHS = [3]byte{0xFE, 0xFF, 0xFF}

	FAT      = byte(0xc)
	Linux    = byte(0x83)
	SquashFS = Linux // SquashFS does not have a dedicated type

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
		SquashFS,
		invalidCHS,
		uint32(8192 + (100 * MB / 512)), // start after partition 1
		uint32(500 * MB / 512),          // 500MB in size

		// partition 3
		inactive,
		invalidCHS,
		SquashFS,
		invalidCHS,
		uint32(8192 + (600 * MB / 512)), // start after partition 2
		uint32(500 * MB / 512),          // 500MB in size

		// partition 4
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

func partitionDevice(o *os.File, path string) error {
	devsize, err := deviceSize(uintptr(o.Fd()))
	if err != nil {
		return err
	}
	log.Printf("device holds %d bytes", devsize)
	if devsize == 0 {
		return fmt.Errorf("path %s does not seem to be a device", path)
	}

	if err := writePartitionTable(o, devsize); err != nil {
		return err
	}

	// Make Linux re-read the partition table. Sequence of system calls like in fdisk(8).
	unix.Sync()

	if err := rereadPartitions(uintptr(o.Fd())); err != nil {
		log.Printf("Re-reading partition table failed: %v. Remember to unplug and re-plug the SD card before creating a file system for persistent data, if desired.", err)
	}

	if err := o.Sync(); err != nil {
		return err
	}

	unix.Sync()
	return nil
}

func mustUnixConn(fd uintptr) *net.UnixConn {
	fc, err := net.FileConn(os.NewFile(fd, ""))
	if err != nil {
		panic(err)
	}
	return fc.(*net.UnixConn)
}

func sudoPartition(path string) (*os.File, error) {
	if fd, err := strconv.Atoi(os.Getenv("GOKR_PACKER_FD")); err == nil {
		// child process
		conn := mustUnixConn(uintptr(fd))
		f, err := os.Create(path)
		if err != nil {
			return nil, err
		}
		if err := partitionDevice(f, path); err != nil {
			return nil, err
		}
		_, _, err = conn.WriteMsgUnix(nil, syscall.UnixRights(int(f.Fd())), nil)
		return nil, err
	}

	pair, err := syscall.Socketpair(syscall.AF_UNIX, syscall.SOCK_STREAM, 0)
	if err != nil {
		return nil, err
	}
	syscall.CloseOnExec(pair[0]) // used in the parent process

	cmd := exec.Command("sudo", append([]string{"--preserve-env"}, os.Args...)...)
	// We cannot use cmd.ExtraFiles with sudo, as sudo closes all file
	// descriptors but stdin, stdout and stderr.
	cmd.Env = []string{"GOKR_PACKER_FD=1"}
	cmd.Stdout = os.NewFile(uintptr(pair[1]), "")
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		return nil, err
	}

	go func() {
		err := cmd.Wait()
		if err != nil {
			log.Fatal(err)
		}
	}()

	// receive file descriptor
	conn := mustUnixConn(uintptr(pair[0]))
	// 32 bytes as per
	// https://github.com/golang/go/blob/21d2e15ee1bed44a7a1b8f775aff4a57cae9533a/src/syscall/syscall_unix_test.go#L177
	oob := make([]byte, 32)
	_, oobn, flags, _, err := conn.ReadMsgUnix(nil, oob)
	if err != nil {
		return nil, err
	}
	if flags != 0 || oobn <= 0 {
		return nil, fmt.Errorf("ReadMsgUnix: flags != 0 || oobn <= 0")
	}

	// file descriptors are now open in this process
	scm, err := syscall.ParseSocketControlMessage(oob[:oobn])
	if err != nil {
		return nil, err
	}
	if got, want := len(scm), 1; got != want {
		return nil, fmt.Errorf("SCM message: got %d, want %d", got, want)
	}

	fds, err := syscall.ParseUnixRights(&scm[0])
	if err != nil {
		return nil, err
	}
	if got, want := len(fds), 1; got != want {
		return nil, fmt.Errorf("ParseUnixRights: got %d fds, want %d fds", got, want)
	}

	return os.NewFile(uintptr(fds[0]), ""), nil
}

func partition(path string) (*os.File, error) {
	if *sudo == "always" {
		return sudoPartition(path)
	}
	o, err := os.Create(path)
	if err != nil {
		if pe, ok := err.(*os.PathError); ok && pe.Err == syscall.EACCES && *sudo == "auto" {
			// permission denied
			log.Printf("Using sudo to gain permission to format %s", path)
			log.Printf("If you prefer, cancel and use: sudo setfacl -m u:${USER}:rw %s", path)
			return sudoPartition(path)
		}
		return nil, err
	}
	return o, partitionDevice(o, path)
}
