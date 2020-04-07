// gokr-packer compiles and installs the specified Go packages as well
// as the gokrazy Go packages and packs them into an SD card image for
// the Raspberry Pi 3.
package main

import (
	"flag"
	"fmt"
	"hash/fnv"
	"io"
	"io/ioutil"
	"log"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/gokrazy/internal/updater"

	// Imported so that the go tool will download the repositories
	_ "github.com/gokrazy/gokrazy/empty"
)

const MB = 1024 * 1024

var (
	overwrite = flag.String("overwrite",
		"",
		"Destination device (e.g. /dev/sdb) or file (e.g. /tmp/gokrazy.img) to overwrite with a full disk image")

	overwriteBoot = flag.String("overwrite_boot",
		"",
		"Destination partition (e.g. /dev/sdb1) or file (e.g. /tmp/boot.fat) to overwrite with the boot file system")

	overwriteRoot = flag.String("overwrite_root",
		"",
		"Destination partition (e.g. /dev/sdb2) or file (e.g. /tmp/root.squashfs) to overwrite with the root file system")

	overwriteMBR = flag.String("overwrite_mbr",
		"",
		"Destination device (e.g. /dev/sdb) or file (e.g. /tmp/mbr.img) to overwrite the MBR of (only effective if -overwrite_boot is specified, too)")

	overwriteInit = flag.String("overwrite_init",
		"",
		"Destination file (e.g. /tmp/init.go) to overwrite with the generated init source code")

	targetStorageBytes = flag.Int("target_storage_bytes",
		0,
		"Number of bytes which the target storage device (SD card) has. Required for using -overwrite=<file>")

	initPkg = flag.String("init_pkg",
		"",
		"Go package to install as /gokrazy/init instead of the auto-generated one")

	update = flag.String("update",
		os.Getenv("GOKRAZY_UPDATE"),
		`URL of a gokrazy installation (e.g. http://gokrazy:mypassword@myhostname/) to update. The special value "yes" uses the stored password and -hostname value to construct the URL`)

	hostname = flag.String("hostname",
		"gokrazy",
		"host name to set on the target system. Will be sent when acquiring DHCP leases")

	gokrazyPkgList = flag.String("gokrazy_pkgs",
		strings.Join([]string{
			"github.com/gokrazy/gokrazy/cmd/dhcp",
			"github.com/gokrazy/gokrazy/cmd/ntp",
			"github.com/gokrazy/gokrazy/cmd/randomd",
		}, ","),
		"comma-separated list of packages installed to /gokrazy/ (boot and system utilities)")

	sudo = flag.String("sudo",
		"auto",
		"whether to elevate privileges using sudo when required (one of auto, always, never, default auto)")
)

var gokrazyPkgs []string

func findCACerts() (string, error) {
	home, err := homedir()
	if err != nil {
		return "", err
	}
	certFiles = append(certFiles, filepath.Join(home, ".config", "gokrazy", "cacert.pem"))
	for _, fn := range certFiles {
		if _, err := os.Stat(fn); err == nil {
			return fn, nil
		}
	}
	return "", fmt.Errorf("did not find any of: %s", strings.Join(certFiles, ", "))
}

type countingWriter int64

func (cw *countingWriter) Write(p []byte) (n int, err error) {
	*cw += countingWriter(len(p))
	return len(p), nil
}

func writeBootFile(bootfilename, mbrfilename string, partuuid uint32) error {
	f, err := os.Create(bootfilename)
	if err != nil {
		return err
	}
	defer f.Close()
	if err := writeBoot(f, mbrfilename, partuuid); err != nil {
		return err
	}
	return f.Close()
}

func writeRootFile(filename string, root *fileInfo) error {
	f, err := os.Create(filename)
	if err != nil {
		return err
	}
	defer f.Close()
	if err := writeRoot(f, root); err != nil {
		return err
	}
	return f.Close()
}

func partitionPath(base, num string) string {
	if strings.HasPrefix(base, "/dev/mmcblk") ||
		strings.HasPrefix(base, "/dev/loop") {
		return base + "p" + num
	} else if strings.HasPrefix(base, "/dev/disk") ||
		strings.HasPrefix(base, "/dev/rdisk") {
		return base + "s" + num
	}
	return base + num
}

func verifyNotMounted(dev string) error {
	b, err := ioutil.ReadFile("/proc/self/mountinfo")
	if err != nil {
		if os.IsNotExist(err) {
			return nil // platform does not have /proc/self/mountinfo, fall back to not verifying
		}
		return err
	}
	for _, line := range strings.Split(strings.TrimSpace(string(b)), "\n") {
		parts := strings.Split(line, " ")
		if len(parts) < 9 {
			continue
		}
		if strings.HasPrefix(parts[9], dev) {
			return fmt.Errorf("partition %s of device %s is mounted", parts[9], dev)
		}
	}
	return nil
}

func overwriteDevice(dev string, root *fileInfo, partuuid uint32) error {
	if err := verifyNotMounted(dev); err != nil {
		return err
	}
	log.Printf("partitioning %s", dev)

	f, err := partition(*overwrite)
	if err != nil {
		return err
	}
	defer f.Close()

	if _, err := f.Seek(8192*512, io.SeekStart); err != nil {
		return err
	}

	if err := writeBoot(f, "", partuuid); err != nil {
		return err
	}

	if err := writeMBR(&offsetReadSeeker{f, 8192 * 512}, f, partuuid); err != nil {
		return err
	}

	if _, err := f.Seek((8192+(100*MB/512))*512, io.SeekStart); err != nil {
		return err
	}

	tmp, err := ioutil.TempFile("", "gokr-packer")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	defer tmp.Close()

	if err := writeRoot(tmp, root); err != nil {
		return err
	}
	if _, err := tmp.Seek(0, io.SeekStart); err != nil {
		return err
	}

	if _, err := io.Copy(f, tmp); err != nil {
		return err
	}

	if err := f.Close(); err != nil {
		return err
	}

	fmt.Printf("If your applications need to store persistent data, unplug and re-plug the SD card, then create a file system using e.g.:\n")
	fmt.Printf("\n")
	fmt.Printf("\tmkfs.ext4 %s\n", partitionPath(dev, "4"))
	fmt.Printf("\n")

	return nil
}

type offsetReadSeeker struct {
	io.ReadSeeker
	offset int64
}

func (ors *offsetReadSeeker) Seek(offset int64, whence int) (int64, error) {
	if whence == io.SeekStart {
		// github.com/gokrazy/internal/fat.Reader only uses io.SeekStart
		return ors.ReadSeeker.Seek(offset+ors.offset, io.SeekStart)
	}
	return ors.ReadSeeker.Seek(offset, whence)
}

func overwriteFile(filename string, root *fileInfo, partuuid uint32) (bootSize int64, rootSize int64, err error) {
	f, err := os.Create(*overwrite)
	if err != nil {
		return 0, 0, err
	}

	if err := f.Truncate(int64(*targetStorageBytes)); err != nil {
		return 0, 0, err
	}

	if err := writePartitionTable(f, uint64(*targetStorageBytes)); err != nil {
		return 0, 0, err
	}

	if _, err := f.Seek(8192*512, io.SeekStart); err != nil {
		return 0, 0, err
	}
	var bs countingWriter
	if err := writeBoot(io.MultiWriter(f, &bs), "", partuuid); err != nil {
		return 0, 0, err
	}

	if err := writeMBR(&offsetReadSeeker{f, 8192 * 512}, f, partuuid); err != nil {
		return 0, 0, err
	}

	if _, err := f.Seek(8192*512+100*MB, io.SeekStart); err != nil {
		return 0, 0, err
	}

	tmp, err := ioutil.TempFile("", "gokr-packer")
	if err != nil {
		return 0, 0, err
	}
	defer os.Remove(tmp.Name())
	defer tmp.Close()

	if err := writeRoot(tmp, root); err != nil {
		return 0, 0, err
	}
	if _, err := tmp.Seek(0, io.SeekStart); err != nil {
		return 0, 0, err
	}

	var rs countingWriter
	if _, err := io.Copy(io.MultiWriter(f, &rs), tmp); err != nil {
		return 0, 0, err
	}

	return int64(bs), int64(rs), f.Close()
}

func derivePartUUID(hostname string) uint32 {
	h := fnv.New32a()
	h.Write([]byte(hostname))
	return h.Sum32()
}

const usage = `
gokr-packer packs gokrazy installations into SD card or file system images.

Usage:
To directly partition and overwrite an SD card:
gokr-packer -overwrite=<device> <go-package> [<go-package>…]

To create an SD card image on the file system:
gokr-packer -overwrite=<file> -target_storage_bytes=<bytes> <go-package> [<go-package>…]

To create a file system image of the boot or root file system:
gokr-packer [-overwrite_boot=<file>|-overwrite_root=<file>] <go-package> [<go-package>…]

To create file system images of both file systems:
gokr-packer -overwrite_boot=<file> -overwrite_root=<file> <go-package> [<go-package>…]

All of the above commands can be combined with the -update flag.

To dump the auto-generated init source code (for use with -init_pkg later):
gokr-packer -overwrite_init=<file> <go-package> [<go-package>…]

Flags:
`

func logic() error {
	dnsCheck := make(chan error)
	go func() {
		defer close(dnsCheck)
		host, err := os.Hostname()
		if err != nil {
			dnsCheck <- fmt.Errorf("finding hostname: %v", err)
			return
		}
		if _, err := net.LookupHost(host); err != nil {
			dnsCheck <- err
			return
		}
		dnsCheck <- nil
	}()

	cacerts, err := findCACerts()
	if err != nil {
		return err
	}

	log.Printf("installing %v", flag.Args())

	if err := install(); err != nil {
		return err
	}

	root, err := findBins()
	if err != nil {
		return err
	}

	if *initPkg == "" {
		if *overwriteInit != "" {
			return dumpInit(*overwriteInit, root)
		}

		tmpdir, err := buildInit(root)
		if err != nil {
			return err
		}
		defer os.RemoveAll(tmpdir)

		gokrazy := root.mustFindDirent("gokrazy")
		gokrazy.dirents = append(gokrazy.dirents, &fileInfo{
			filename: "init",
			fromHost: filepath.Join(tmpdir, "init"),
		})
	}

	defaultPassword := ""
	if *update != "" {
		if u, err := url.Parse(*update); err == nil {
			defaultPassword, _ = u.User.Password()
		}
	}
	pw, pwPath, err := ensurePasswordFileExists(defaultPassword)
	if err != nil {
		return err
	}

	for _, dir := range []string{"dev", "etc", "proc", "sys", "tmp", "perm"} {
		root.dirents = append(root.dirents, &fileInfo{
			filename: dir,
		})
	}

	etc := root.mustFindDirent("etc")
	tmpdir, err := ioutil.TempDir("", "gokrazy")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmpdir)
	hostLocaltime, err := hostLocaltime(tmpdir)
	if err != nil {
		return err
	}
	if hostLocaltime != "" {
		etc.dirents = append(etc.dirents, &fileInfo{
			filename: "localtime",
			fromHost: hostLocaltime,
		})
	}
	etc.dirents = append(etc.dirents, &fileInfo{
		filename:    "resolv.conf",
		symlinkDest: "/tmp/resolv.conf",
	})
	etc.dirents = append(etc.dirents, &fileInfo{
		filename: "hosts",
		fromLiteral: `127.0.0.1 localhost
::1 localhost
`,
	})
	etc.dirents = append(etc.dirents, &fileInfo{
		filename:    "hostname",
		fromLiteral: *hostname,
	})

	ssl := &fileInfo{filename: "ssl"}
	ssl.dirents = append(ssl.dirents, &fileInfo{
		filename: "ca-bundle.pem",
		fromHost: cacerts,
	})
	etc.dirents = append(etc.dirents, ssl)

	etc.dirents = append(etc.dirents, &fileInfo{
		filename: "gokr-pw.txt",
		fromHost: pwPath,
	})

	partuuid := derivePartUUID(*hostname)

	// Determine where to write the boot and root images to.
	var (
		isDev                    bool
		tmpBoot, tmpRoot, tmpMBR *os.File
		bootSize, rootSize       int64
	)
	switch {
	case *overwrite != "":
		st, err := os.Lstat(*overwrite)
		if err != nil && !os.IsNotExist(err) {
			return err
		}

		isDev = err == nil && st.Mode()&os.ModeDevice == os.ModeDevice

		if isDev {
			if err := overwriteDevice(*overwrite, root, partuuid); err != nil {
				return err
			}
			fmt.Printf("To boot gokrazy, plug the SD card into a Raspberry Pi 3 (no other model supported)\n")
			fmt.Printf("\n")
		} else {
			lower := 1100*MB + 8192

			if *targetStorageBytes == 0 {
				return fmt.Errorf("-target_storage_bytes is required (e.g. -target_storage_bytes=%d) when using -overwrite with a file", lower)
			}
			if *targetStorageBytes%512 != 0 {
				return fmt.Errorf("-target_storage_bytes must be a multiple of 512 (sector size), use e.g. %d", lower)
			}
			if *targetStorageBytes < lower {
				return fmt.Errorf("-target_storage_bytes must be at least %d (for boot + 2 root file systems)", lower)
			}

			bootSize, rootSize, err = overwriteFile(*overwrite, root, partuuid)
			if err != nil {
				return err
			}

			fmt.Printf("To boot gokrazy, copy %s to an SD card and plug it into a Raspberry Pi 3 (no other model supported)\n", *overwrite)
			fmt.Printf("\n")
		}

	default:
		if *overwriteBoot != "" {
			mbrfn := *overwriteMBR
			if *overwriteMBR == "" {
				tmpMBR, err = ioutil.TempFile("", "gokrazy")
				if err != nil {
					return err
				}
				defer os.Remove(tmpMBR.Name())
				mbrfn = tmpMBR.Name()
			}
			if err := writeBootFile(*overwriteBoot, mbrfn, partuuid); err != nil {
				return err
			}
		}

		if *overwriteRoot != "" {
			if err := writeRootFile(*overwriteRoot, root); err != nil {
				return err
			}
		}

		if *overwriteBoot == "" && *overwriteRoot == "" {
			tmpMBR, err = ioutil.TempFile("", "gokrazy")
			if err != nil {
				return err
			}
			defer os.Remove(tmpMBR.Name())

			tmpBoot, err = ioutil.TempFile("", "gokrazy")
			if err != nil {
				return err
			}
			defer os.Remove(tmpBoot.Name())

			if err := writeBoot(tmpBoot, tmpMBR.Name(), partuuid); err != nil {
				return err
			}

			tmpRoot, err = ioutil.TempFile("", "gokrazy")
			if err != nil {
				return err
			}
			defer os.Remove(tmpRoot.Name())

			if err := writeRoot(tmpRoot, root); err != nil {
				return err
			}
		}
	}

	fmt.Printf("To interact with the device, gokrazy provides a web interface reachable at:\n")
	fmt.Printf("\n")
	fmt.Printf("\thttp://gokrazy:%s@%s/\n", pw, *hostname)
	fmt.Printf("\n")
	fmt.Printf("There will not be any other output (no HDMI, no serial console, etc.)\n")

	if err := <-dnsCheck; err != nil {
		fmt.Printf("\nWARNING: if the above URL does not work, perhaps name resolution (DNS) is broken\n")
		fmt.Printf("in your local network? Resolving your hostname failed: %v\n", err)
		fmt.Printf("Did you maybe configure a DNS server other than your router?\n\n")
	}

	if *update == "" {
		return nil
	}

	// Determine where to read the boot, root and MBR images from.
	var rootReader, bootReader, mbrReader io.Reader
	switch {
	case *overwrite != "":
		if isDev {
			bootFile, err := os.Open(*overwrite + "1")
			if err != nil {
				return err
			}
			bootReader = bootFile
			rootFile, err := os.Open(*overwrite + "2")
			if err != nil {
				return err
			}
			rootReader = rootFile
		} else {
			bootFile, err := os.Open(*overwrite)
			if err != nil {
				return err
			}
			if _, err := bootFile.Seek(8192*512, io.SeekStart); err != nil {
				return err
			}
			bootReader = &io.LimitedReader{
				R: bootFile,
				N: bootSize,
			}

			rootFile, err := os.Open(*overwrite)
			if err != nil {
				return err
			}
			if _, err := rootFile.Seek(8192*512+100*MB, io.SeekStart); err != nil {
				return err
			}
			rootReader = &io.LimitedReader{
				R: rootFile,
				N: rootSize,
			}
		}
		mbrFile, err := os.Open(*overwrite)
		if err != nil {
			return err
		}
		mbrReader = &io.LimitedReader{
			R: mbrFile,
			N: 446,
		}

	default:
		if *overwriteBoot != "" {
			bootFile, err := os.Open(*overwriteBoot)
			if err != nil {
				return err
			}
			bootReader = bootFile
			if *overwriteMBR != "" {
				mbrFile, err := os.Open(*overwriteMBR)
				if err != nil {
					return err
				}
				mbrReader = mbrFile
			} else {
				if _, err := tmpMBR.Seek(0, io.SeekStart); err != nil {
					return err
				}
				mbrReader = tmpMBR
			}
		}

		if *overwriteRoot != "" {
			rootFile, err := os.Open(*overwriteRoot)
			if err != nil {
				return err
			}
			rootReader = rootFile
		}

		if *overwriteBoot == "" && *overwriteRoot == "" {
			if _, err := tmpBoot.Seek(0, io.SeekStart); err != nil {
				return err
			}
			bootReader = tmpBoot

			if _, err := tmpMBR.Seek(0, io.SeekStart); err != nil {
				return err
			}
			mbrReader = tmpMBR

			if _, err := tmpRoot.Seek(0, io.SeekStart); err != nil {
				return err
			}
			rootReader = tmpRoot
		}
	}

	if *update == "yes" {
		*update = "http://gokrazy:" + pw + "@" + *hostname + "/"
	}

	baseUrl, err := url.Parse(*update)
	if err != nil {
		return err
	}
	baseUrl.Path = "/"
	log.Printf("Updating %q", *update)

	// Start with the root file system because writing to the non-active
	// partition cannot break the currently running system.
	if err := updater.UpdateRoot(baseUrl.String(), rootReader); err != nil {
		return fmt.Errorf("updating root file system: %v", err)
	}

	if err := updater.UpdateBoot(baseUrl.String(), bootReader); err != nil {
		return fmt.Errorf("updating boot file system: %v", err)
	}

	if err := updater.UpdateMBR(baseUrl.String(), mbrReader); err != nil {
		if err == updater.ErrUpdateHandlerNotImplemented {
			log.Printf("target does not support updating MBR yet, ignoring")
		} else {
			return fmt.Errorf("updating MBR: %v", err)
		}
	}

	if err := updater.Switch(baseUrl.String()); err != nil {
		return fmt.Errorf("switching to non-active partition: %v", err)
	}

	if err := updater.Reboot(baseUrl.String()); err != nil {
		return fmt.Errorf("reboot: %v", err)
	}

	log.Printf("updated, should be back within 10 seconds")
	return nil
}

func main() {
	flag.Usage = func() {
		fmt.Fprintf(os.Stderr, usage)
		flag.PrintDefaults()
		os.Exit(2)
	}
	flag.Parse()
	log.SetFlags(log.LstdFlags | log.Lshortfile)

	gokrazyPkgs = strings.Split(*gokrazyPkgList, ",")

	if *overwrite == "" && *overwriteBoot == "" && *overwriteRoot == "" && *overwriteInit == "" && *update == "" {
		flag.Usage()
	}

	if os.Getenv("GOKR_PACKER_FD") != "" { // partitioning child process
		if _, err := sudoPartition(*overwrite); err != nil {
			log.Fatal(err)
		}
		os.Exit(0)
	}

	if err := logic(); err != nil {
		log.Fatal(err)
	}
}
