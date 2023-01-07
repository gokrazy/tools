// Package oldpacker contains the code of cmd/gokr-packer, so that we can run it
// from our integration tests.
package oldpacker

import (
	"flag"
	"fmt"
	"log"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	// Imported so that the go tool will download the repositories
	_ "github.com/gokrazy/gokrazy/empty"

	"github.com/gokrazy/internal/config"
	"github.com/gokrazy/internal/tlsflag"
	"github.com/gokrazy/internal/updateflag"
	internalpacker "github.com/gokrazy/tools/internal/packer"
	"github.com/gokrazy/tools/packer"
)

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

	hostname = flag.String("hostname",
		"gokrazy",
		"Host name to set on the target system. Will be sent when acquiring DHCP leases")

	// TODO: Generate unique hostname on bootstrap e.g. gokrazy-<5-10 random characters>?

	gokrazyPkgList = flag.String("gokrazy_pkgs",
		strings.Join([]string{
			"github.com/gokrazy/gokrazy/cmd/dhcp",
			"github.com/gokrazy/gokrazy/cmd/ntp",
			"github.com/gokrazy/gokrazy/cmd/randomd",
		}, ","),
		"Comma-separated list of packages installed to /gokrazy/ (boot and system utilities)")

	sudo = flag.String("sudo",
		"auto",
		"Whether to elevate privileges using sudo when required (one of auto, always, never, default auto)")

	httpPort = flag.String("http_port",
		"80",
		"HTTP port for gokrazy to listen on")

	httpsPort = flag.String("https_port",
		"443",
		"HTTPS (TLS) port for gokrazy to listen on")

	testboot = flag.Bool("testboot",
		false,
		"Trigger a testboot instead of switching to the new root partition directly")

	deviceType = flag.String("device_type",
		"",
		`Device type identifier (defined in github.com/gokrazy/internal/deviceconfig) used for applying device-specific modifications to gokrazy.
e.g. -device_type=odroidhc1 to apply MBR changes and device-specific bootloader files for Odroid XU4/HC1/HC2.
Defaults to an empty string.`)

	serialConsole = flag.String("serial_console",
		"serial0,115200",
		`"serial0,115200" enables UART0 as a serial console, "disabled" allows applications to use UART0 instead, "off" sets enable_uart=0 in config.txt for the Raspberry Pi firmware`)

	kernelPackage = flag.String("kernel_package",
		"github.com/gokrazy/kernel",
		"Go package to copy vmlinuz and *.dtb from for constructing the firmware file system")

	firmwarePackage = flag.String("firmware_package",
		"github.com/gokrazy/firmware",
		"Go package to copy *.{bin,dat,elf} from for constructing the firmware file system")

	eepromPackage = flag.String("eeprom_package",
		"github.com/gokrazy/rpi-eeprom",
		"Go package to copy *.bin from for constructing the firmware file system")

	writeInstanceConfig = flag.String("write_instance_config",
		"",
		"instance, identified by hostname. $INSTANCE/config.json will be written based on the other flags. See https://github.com/gokrazy/gokrazy/issues/147 for more details.")
)

var gokrazyPkgs []string

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

func logic(instanceDir string) error {
	if !updateflag.NewInstallation() && *overwrite != "" {
		return fmt.Errorf("both -update and -overwrite are specified; use either one, not both")
	}

	cfg := config.Struct{
		Packages:   flag.Args(),
		Hostname:   *hostname,
		DeviceType: *deviceType,
		Update: &config.UpdateStruct{
			HTTPPort:  *httpPort,
			HTTPSPort: *httpsPort,
			UseTLS:    tlsflag.GetUseTLS(),
		},
		SerialConsole:   *serialConsole,
		GokrazyPackages: &gokrazyPkgs,
		KernelPackage:   kernelPackage,
		FirmwarePackage: firmwarePackage,
		EEPROMPackage:   eepromPackage,
		InternalCompatibilityFlags: &config.InternalCompatibilityFlags{
			Overwrite:          *overwrite,
			OverwriteBoot:      *overwriteBoot,
			OverwriteMBR:       *overwriteMBR,
			OverwriteRoot:      *overwriteRoot,
			TargetStorageBytes: *targetStorageBytes,
			OverwriteInit:      *overwriteInit,
			InitPkg:            *initPkg,
			Testboot:           *testboot,
			Sudo:               *sudo,
			Update:             updateflag.GetUpdate(),
			Insecure:           tlsflag.GetInsecure(),
		},
	}

	// Convert common -update URLs (changing the hostname, changing the
	// password, changing the HTTP port) to their corresponding config.Update
	// fields.
	defaultPassword, updateHostname := updateflag.GetUpdateTarget(*hostname)
	constructed := "http://gokrazy:" + defaultPassword + "@" + updateHostname + "/"
	if canonical, err := url.Parse(updateflag.GetUpdate()); err == nil {
		// Ensure both URLs (constructed and -update) end in a trailing slash.
		canonical.Path = "/"
		if constructed == canonical.String() {
			cfg.Update.HTTPPassword = defaultPassword
			if updateHostname != *hostname {
				cfg.Update.Hostname = updateHostname
			}
			if strings.HasSuffix(cfg.Update.Hostname, ":"+cfg.Update.HTTPPort) {
				cfg.Update.Hostname = strings.TrimSuffix(cfg.Update.Hostname, ":"+cfg.Update.HTTPPort)
			}
			cfg.InternalCompatibilityFlags.Update = "yes"
		}
	}

	// PerPackageConfigForMigration expects cfg.Packages and cfg.GokrazyPackages
	// to be set, and cfg.PackageConfig to not be set yet.
	packageConfig, err := internalpacker.PerPackageConfigForMigration(&cfg)
	if err != nil {
		return err
	}
	cfg.PackageConfig = packageConfig

	if *writeInstanceConfig != "" {
		// default value? empty the flag to exclude it from the config file
		if cfg.Update.HTTPPort == "80" {
			cfg.Update.HTTPPort = ""
		}
		if cfg.Update.HTTPSPort == "443" {
			cfg.Update.HTTPSPort = ""
		}
		if cfg.InternalCompatibilityFlags.Sudo == "auto" {
			cfg.InternalCompatibilityFlags.Sudo = ""
		}
		if cfg.InternalCompatibilityFlags.Update == "yes" {
			cfg.InternalCompatibilityFlags.Update = ""
		}

		configJSON := filepath.Join(instanceDir, *writeInstanceConfig, "config.json")
		fmt.Printf("writing config.json to %s\n", configJSON)

		b, err := cfg.FormatForFile()
		if err != nil {
			return err
		}
		if err := os.MkdirAll(filepath.Dir(configJSON), 0755); err != nil {
			return err
		}
		if err := os.WriteFile(configJSON, b, 0600); err != nil {
			return err
		}

		return nil
	}

	internalpacker.Main(&cfg)
	return nil
}

func Main() {
	flag.Usage = func() {
		fmt.Fprintf(os.Stderr, usage)
		flag.PrintDefaults()
		os.Exit(2)
	}
	updateflag.RegisterFlags(flag.CommandLine, "update")
	tlsflag.RegisterFlags(flag.CommandLine)

	def := os.Getenv("GOKRAZY_PARENT_DIR")
	if def == "" {
		homeDir, err := os.UserHomeDir()
		if err != nil {
			homeDir = fmt.Sprintf("os.UserHomeDir failed: %v", err)
		}
		def = filepath.Join(homeDir, "gokrazy")
	}

	instanceDir := flag.String(
		"instance_dir",
		def,
		`instance, identified by hostname`)

	flag.Parse()

	if *gokrazyPkgList != "" {
		gokrazyPkgs = strings.Split(*gokrazyPkgList, ",")
	}

	if *overwrite == "" && *overwriteBoot == "" && *overwriteRoot == "" && *overwriteInit == "" && updateflag.NewInstallation() {
		flag.Usage()
	}

	if os.Getenv("GOKR_PACKER_FD") != "" { // partitioning child process
		p := internalpacker.Pack{
			Pack: packer.NewPackForHost(*hostname),
		}

		if _, err := p.SudoPartition(*overwrite); err != nil {
			log.Fatal(err)
		}
		os.Exit(0)
	}

	if err := logic(*instanceDir); err != nil {
		log.Fatal(err)
	}
}
