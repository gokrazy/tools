package packer

import (
	"fmt"
	"log"
	"net"
	"os"
	"os/exec"
	"strconv"
	"syscall"
)

func (p *Pack) partitionDevice(o *os.File, path string) error {
	devsize, err := deviceSize(uintptr(o.Fd()))
	if err != nil {
		return err
	}
	log.Printf("device holds %d bytes", devsize)
	if devsize == 0 {
		return fmt.Errorf("path %s does not seem to be a device", path)
	}

	if err := p.Partition(o, devsize); err != nil {
		return err
	}

	return p.RereadPartitions(o)
}

func mustUnixConn(fd uintptr) *net.UnixConn {
	fc, err := net.FileConn(os.NewFile(fd, ""))
	if err != nil {
		panic(err)
	}
	return fc.(*net.UnixConn)
}

func (p *Pack) SudoPartition(path string) (*os.File, error) {
	if fd, err := strconv.Atoi(os.Getenv("GOKR_PACKER_FD")); err == nil {
		// child process
		conn := mustUnixConn(uintptr(fd))
		f, err := os.Create(path)
		if err != nil {
			return nil, err
		}
		if err := p.partitionDevice(f, path); err != nil {
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

	// Use absolute path because $PATH might not be the same when using sudo:
	if exe, err := os.Executable(); err == nil {
		os.Args[0] = exe
	}

	cmd := exec.Command("sudo", append([]string{"--preserve-env"}, os.Args...)...)
	// We cannot use cmd.ExtraFiles with sudo, as sudo closes all file
	// descriptors but stdin, stdout and stderr.
	cmd.Env = []string{
		"GOKR_PACKER_FD=1",
		fmt.Sprintf("HOME=%s", os.Getenv("HOME")), // for instance config detection
	}
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
	_, oobn, _, _, err := conn.ReadMsgUnix(nil, oob)
	if err != nil {
		return nil, err
	}
	if oobn <= 0 {
		return nil, fmt.Errorf("ReadMsgUnix: oobn <= 0")
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

func (p *Pack) partition(path string) (*os.File, error) {
	if p.Cfg.InternalCompatibilityFlags.Sudo == "always" {
		return p.SudoPartition(path)
	}
	o, err := os.Create(path)
	if err != nil {
		pe, ok := err.(*os.PathError)
		if ok && pe.Err == syscall.EACCES && p.Cfg.InternalCompatibilityFlags.Sudo == "auto" {
			// permission denied
			log.Printf("Using sudo to gain permission to format %s", path)
			log.Printf("If you prefer, cancel and use: sudo setfacl -m u:${USER}:rw %s", path)
			return p.SudoPartition(path)
		}
		if ok && pe.Err == syscall.EROFS {
			log.Printf("%s read-only; check if you have a physical write-protect switch on your SD card?", path)
			return nil, err
		}
		return nil, err
	}
	return o, p.partitionDevice(o, path)
}
