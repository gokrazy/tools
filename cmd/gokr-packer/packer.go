// gokr-packer compiles and installs the specified Go packages as well
// as the gokrazy Go packages and packs them into an SD card image for
// the Raspberry Pi 3.
package main

import (
	"flag"
	"fmt"
	"io"
	"io/ioutil"
	"log"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/gokrazy/internal/updater"

	// Imported so that the go tool will download the repositories
	_ "github.com/gokrazy/firmware"
	_ "github.com/gokrazy/gokrazy/empty"
	_ "github.com/gokrazy/kernel"
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
		"Destination partition (e.g. /dev/sdb2) or file (e.g. /tmp/root.fat) to overwrite with the root file system")

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
		"",
		`URL of a gokrazy installation (e.g. http://gokrazy:mypassword@myhostname/) to update. The special value "yes" uses the stored password and -hostname value to construct the URL`)

	hostname = flag.String("hostname",
		"gokrazy",
		"host name to set on the target system. Will be sent when acquiring DHCP leases")
)

var gokrazyPkgs = []string{
	// boot and system utilities
	"github.com/gokrazy/gokrazy/cmd/...",
}

func findCACerts() (string, error) {
	// From go1.8/src/crypto/x509/root_linux.go
	var certFiles = []string{
		"/etc/ssl/certs/ca-certificates.crt",                // Debian/Ubuntu/Gentoo etc.
		"/etc/pki/ca-trust/extracted/pem/tls-ca-bundle.pem", // CentOS/RHEL 7
		"/etc/pki/tls/certs/ca-bundle.crt",                  // Fedora/RHEL 6
		"/etc/ssl/ca-bundle.pem",                            // OpenSUSE
		"/etc/pki/tls/cacert.pem",                           // OpenELEC
	}
	for _, fn := range certFiles {
		if _, err := os.Stat(fn); err == nil {
			return fn, nil
		}
	}
	return "", fmt.Errorf("did not found any of: %s", strings.Join(certFiles, ", "))
}

type countingWriter int64

func (cw *countingWriter) Write(p []byte) (n int, err error) {
	*cw += countingWriter(len(p))
	return len(p), nil
}

func writeBootFile(filename string) error {
	f, err := os.Create(filename)
	if err != nil {
		return err
	}
	defer f.Close()
	if err := writeBoot(f); err != nil {
		return err
	}
	return f.Close()
}

func writeRootFile(filename string, bins map[string]string) error {
	f, err := os.Create(filename)
	if err != nil {
		return err
	}
	defer f.Close()
	if err := writeRoot(f, bins); err != nil {
		return err
	}
	return f.Close()
}

func partitionPath(base, num string) string {
	if strings.HasPrefix(base, "/dev/mmcblk") {
		return base + "p" + num
	}
	return base + num
}

func overwriteDevice(dev string, bins map[string]string) error {
	log.Printf("partitioning %s", dev)

	if err := partition(*overwrite); err != nil {
		return err
	}

	// TODO: get rid of this ridiculous sleep. Without it, I get -EACCES when
	// trying to open /dev/sdb1.
	log.Printf("waiting for %s1 to appear", dev)
	time.Sleep(1 * time.Second)

	if err := writeBootFile(partitionPath(*overwrite, "1")); err != nil {
		return err
	}

	if err := writeRootFile(partitionPath(*overwrite, "2"), bins); err != nil {
		return err
	}

	log.Printf("mkfs.ext4 %s4", *overwrite)

	return nil
}

func overwriteFile(filename string, bins map[string]string) (bootSize int64, rootSize int64, err error) {
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
	if err := writeBoot(io.MultiWriter(f, &bs)); err != nil {
		return 0, 0, err
	}

	if _, err := f.Seek(8192*512+100*MB, io.SeekStart); err != nil {
		return 0, 0, err
	}
	var rs countingWriter
	if err := writeRoot(io.MultiWriter(f, &rs), bins); err != nil {
		return 0, 0, err
	}

	return int64(bs), int64(rs), f.Close()
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

func main() {
	flag.Usage = func() {
		fmt.Fprintf(os.Stderr, usage)
		flag.PrintDefaults()
		os.Exit(2)
	}
	flag.Parse()
	log.SetFlags(log.LstdFlags | log.Lshortfile)

	if *overwrite == "" && *overwriteBoot == "" && *overwriteRoot == "" && *overwriteInit == "" && *update == "" {
		flag.Usage()
	}

	cacerts, err := findCACerts()
	if err != nil {
		log.Fatal(err)
	}

	log.Printf("installing %v", flag.Args())

	if err := install(); err != nil {
		log.Fatal(err)
	}

	bins, err := findBins()
	if err != nil {
		log.Fatal(err)
	}

	if *initPkg == "" {
		if *overwriteInit != "" {
			if err := dumpInit(*overwriteInit, bins); err != nil {
				log.Fatal(err)
			}
			return
		}

		tmpdir, err := buildInit(bins)
		if err != nil {
			log.Fatal(err)
		}
		defer os.RemoveAll(tmpdir)

		bins["/gokrazy/init"] = filepath.Join(tmpdir, "init")
	}

	pw, pwPath, err := ensurePasswordFileExists()
	if err != nil {
		log.Fatal(err)
	}

	bins["/localtim"] = "/etc/localtime"
	bins["/cacerts"] = cacerts
	bins["/gokr-pw.txt"] = pwPath
	bins["/dev/"] = ""
	bins["/etc/"] = ""
	bins["/proc/"] = ""
	bins["/sys/"] = ""
	bins["/tmp/"] = ""
	bins["/perm/"] = ""

	// Determine where to write the boot and root images to.
	var (
		isDev              bool
		tmpBoot, tmpRoot   *os.File
		bootSize, rootSize int64
	)
	switch {
	case *overwrite != "":
		st, err := os.Lstat(*overwrite)
		if err != nil && !os.IsNotExist(err) {
			log.Fatal(err)
		}

		isDev := err == nil && st.Mode()&os.ModeDevice == os.ModeDevice

		if isDev {
			if err := overwriteDevice(*overwrite, bins); err != nil {
				log.Fatal(err)
			}
		} else {
			if *targetStorageBytes == 0 {
				log.Fatalf("-target_storage_bytes is required when using -overwrite with a file")
			}
			if *targetStorageBytes%512 != 0 {
				log.Fatalf("-target_storage_bytes must be a multiple of 512 (sector size)")
			}
			if lower := 1100*MB + 8192; *targetStorageBytes < lower {
				log.Fatalf("-target_storage_bytes must be at least %d (for boot + 2 root file systems)", lower)
			}

			bootSize, rootSize, err = overwriteFile(*overwrite, bins)
			if err != nil {
				log.Fatal(err)
			}
		}

	default:
		switch {
		case *overwriteBoot != "":
			if err := writeBootFile(*overwriteBoot); err != nil {
				log.Fatal(err)
			}

		case *overwriteRoot != "":
			if err := writeRootFile(*overwriteRoot, bins); err != nil {
				log.Fatal(err)
			}

		default:
			tmpBoot, err = ioutil.TempFile("", "gokrazy")
			if err != nil {
				log.Fatal(err)
			}
			defer os.Remove(tmpBoot.Name())

			if err := writeBoot(tmpBoot); err != nil {
				log.Fatal(err)
			}

			tmpRoot, err = ioutil.TempFile("", "gokrazy")
			if err != nil {
				log.Fatal(err)
			}
			defer os.Remove(tmpRoot.Name())

			if err := writeRoot(tmpRoot, bins); err != nil {
				log.Fatal(err)
			}
		}
	}

	log.Printf("http://gokrazy:%s@%s/", pw, *hostname)

	if *update == "" {
		return
	}

	// Determine where to read the boot and root images from.
	var rootReader, bootReader io.Reader
	switch {
	case *overwrite != "":
		if isDev {
			bootFile, err := os.Open(*overwrite + "1")
			if err != nil {
				log.Fatal(err)
			}
			bootReader = bootFile
			rootFile, err := os.Open(*overwrite + "2")
			if err != nil {
				log.Fatal(err)
			}
			rootReader = rootFile
		} else {
			bootFile, err := os.Open(*overwrite)
			if err != nil {
				log.Fatal(err)
			}
			if _, err := bootFile.Seek(8192*512, io.SeekStart); err != nil {
				log.Fatal(err)
			}
			bootReader = &io.LimitedReader{
				R: bootFile,
				N: rootSize,
			}

			rootFile, err := os.Open(*overwrite)
			if err != nil {
				log.Fatal(err)
			}
			if _, err := rootFile.Seek(8192*512+100*MB, io.SeekStart); err != nil {
				log.Fatal(err)
			}
			rootReader = &io.LimitedReader{
				R: rootFile,
				N: bootSize,
			}
		}

	default:
		switch {
		case *overwriteBoot != "":
			bootFile, err := os.Open(*overwriteBoot)
			if err != nil {
				log.Fatal(err)
			}
			bootReader = bootFile

		case *overwriteRoot != "":
			rootFile, err := os.Open(*overwriteRoot)
			if err != nil {
				log.Fatal(err)
			}
			rootReader = rootFile

		default:
			if _, err := tmpBoot.Seek(0, io.SeekStart); err != nil {
				log.Fatal(err)
			}
			bootReader = tmpBoot

			if _, err := tmpRoot.Seek(0, io.SeekStart); err != nil {
				log.Fatal(err)
			}
			rootReader = tmpRoot
		}
	}

	if *update == "yes" {
		*update = "http://gokrazy:" + pw + "@" + *hostname + "/"
	}

	baseUrl, err := url.Parse(*update)
	if err != nil {
		log.Fatal(err)
	}
	baseUrl.Path = "/"
	log.Printf("Updating %q", *update)

	// Start with the root file system because writing to the non-active
	// partition cannot break the currently running system.
	if err := updater.UpdateRoot(baseUrl.String(), rootReader); err != nil {
		log.Fatalf("updating root file system: %v", err)
	}

	if err := updater.UpdateBoot(baseUrl.String(), bootReader); err != nil {
		log.Fatalf("updating boot file system: %v", err)
	}

	if err := updater.Switch(baseUrl.String()); err != nil {
		log.Fatalf("switching to non-active partition: %v", err)
	}

	if err := updater.Reboot(baseUrl.String()); err != nil {
		log.Fatalf("reboot: %v", err)
	}

	log.Printf("updated, should be back within 10 seconds")
}
