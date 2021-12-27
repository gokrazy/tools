// gokr-packer compiles and installs the specified Go packages as well
// as the gokrazy Go packages and packs them into an SD card image for
// the Raspberry Pi 3.
package main

import (
	"bufio"
	"context"
	"flag"
	"fmt"
	"io"
	"io/ioutil"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"sort"
	"strings"
	"time"

	// Imported so that the go tool will download the repositories
	_ "github.com/gokrazy/gokrazy/empty"

	"github.com/gokrazy/internal/httpclient"
	"github.com/gokrazy/internal/humanize"
	"github.com/gokrazy/internal/progress"
	"github.com/gokrazy/internal/updateflag"
	"github.com/gokrazy/tools/internal/measure"
	"github.com/gokrazy/tools/packer"
	"github.com/gokrazy/updater"
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

	hostname = flag.String("hostname",
		"gokrazy",
		"Host name to set on the target system. Will be sent when acquiring DHCP leases")

	// TODO: Generate unique hostname on bootstrap e.g. gokrazy-<5-10 random characters>?
	useTLS = flag.String("tls",
		"",
		`TLS certificate for the web interface (-tls=<certificate or full chain path>,<private key path>).
Use -tls=self-signed to generate a self-signed RSA4096 certificate using the hostname specified with -hostname. In this case, the certificate and key will be placed in your local config folder (on Linux: ~/.config/gokrazy/<hostname>/).
WARNING: When reusing a hostname, no new certificate will be generated and the stored one will be used.
When updating a running instance, the specified certificate will be used to verify the connection. Otherwise the updater will load the hostname-specific certificate from your local config folder in addition to the system trust store.
You can also create your own certificate-key-pair (e.g. by using https://github.com/FiloSottile/mkcert) and place them into your local config folder.`,
	)

	tlsInsecure = flag.Bool("insecure",
		false,
		"Ignore TLS stripping detection.")

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
)

var gokrazyPkgs []string

type filePathAndModTime struct {
	path    string
	modTime time.Time
}

func findPackageFiles(fileType string) ([]filePathAndModTime, error) {
	var packageFilePaths []filePathAndModTime
	err := filepath.Walk(fileType, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info != nil && !info.Mode().IsRegular() {
			return nil
		}
		if strings.HasSuffix(path, fmt.Sprintf("/%s.txt", fileType)) {
			packageFilePaths = append(packageFilePaths, filePathAndModTime{
				path:    path,
				modTime: info.ModTime(),
			})
		}
		return nil
	})
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil // no fileType directory found
		}
	}

	return packageFilePaths, nil
}

type packageConfigFile struct {
	kind         string
	path         string
	lastModified time.Time
}

// packageConfigFiles is a map from package path to packageConfigFile, for constructing output that is keyed per package
var packageConfigFiles = make(map[string][]packageConfigFile)

func buildPackagesFromFlags() map[string]bool {
	buildPackages := make(map[string]bool)
	for _, pkg := range flag.Args() {
		buildPackages[pkg] = true
	}
	for _, pkg := range gokrazyPkgs {
		buildPackages[pkg] = true
	}
	return buildPackages
}

func findFlagFiles() (map[string]string, error) {
	flagFilePaths, err := findPackageFiles("flags")
	if err != nil {
		return nil, err
	}

	if len(flagFilePaths) == 0 {
		return nil, nil // no flags.txt files found
	}

	buildPackages := buildPackagesFromFlags()

	contents := make(map[string]string)
	for _, p := range flagFilePaths {
		pkg := strings.TrimSuffix(strings.TrimPrefix(p.path, "flags/"), "/flags.txt")
		if !buildPackages[pkg] {
			log.Printf("WARNING: flag file %s does not match any specified package (%s)", pkg, flag.Args())
			continue
		}
		packageConfigFiles[pkg] = append(packageConfigFiles[pkg], packageConfigFile{
			kind:         "be started with command-line flags",
			path:         p.path,
			lastModified: p.modTime,
		})

		b, err := ioutil.ReadFile(p.path)
		if err != nil {
			return nil, err
		}
		// NOTE: ideally we would use the full package here, but our init
		// template only deals with base names right now.
		contents[filepath.Base(pkg)] = string(b)
	}

	return contents, nil
}

func findBuildFlagsFiles() (map[string][]string, error) {
	buildFlagsFilePaths, err := findPackageFiles("buildflags")
	if err != nil {
		return nil, err
	}

	if len(buildFlagsFilePaths) == 0 {
		return nil, nil // no flags.txt files found
	}

	buildPackages := buildPackagesFromFlags()

	contents := make(map[string][]string)
	for _, p := range buildFlagsFilePaths {
		pkg := strings.TrimSuffix(strings.TrimPrefix(p.path, "buildflags/"), "/buildflags.txt")
		if !buildPackages[pkg] {
			log.Printf("WARNING: buildflags file %s does not match any specified package (%s)", pkg, flag.Args())
			continue
		}
		packageConfigFiles[pkg] = append(packageConfigFiles[pkg], packageConfigFile{
			kind:         "be compiled with build flags",
			path:         p.path,
			lastModified: p.modTime,
		})

		b, err := ioutil.ReadFile(p.path)
		if err != nil {
			return nil, err
		}

		var buildFlags []string
		sc := bufio.NewScanner(strings.NewReader(string(b)))
		for sc.Scan() {
			if flag := sc.Text(); flag != "" {
				buildFlags = append(buildFlags, flag)
			}
		}

		if err := sc.Err(); err != nil {
			return nil, err
		}

		// use full package path opposed to flags
		contents[pkg] = buildFlags
	}

	return contents, nil
}

func findEnvFiles() (map[string]string, error) {
	buildFlagsFilePaths, err := findPackageFiles("env")
	if err != nil {
		return nil, err
	}

	if len(buildFlagsFilePaths) == 0 {
		return nil, nil // no flags.txt files found
	}

	buildPackages := buildPackagesFromFlags()

	contents := make(map[string]string)
	for _, p := range buildFlagsFilePaths {
		pkg := strings.TrimSuffix(strings.TrimPrefix(p.path, "env/"), "/env.txt")
		if !buildPackages[pkg] {
			log.Printf("WARNING: environment variable file %s does not match any specified package (%s)", pkg, flag.Args())
			continue
		}
		packageConfigFiles[pkg] = append(packageConfigFiles[pkg], packageConfigFile{
			kind:         "be started with environment variables",
			path:         p.path,
			lastModified: p.modTime,
		})

		b, err := ioutil.ReadFile(p.path)
		if err != nil {
			return nil, err
		}
		// NOTE: ideally we would use the full package here, but our init
		// template only deals with base names right now.
		contents[filepath.Base(pkg)] = string(b)
	}

	return contents, nil
}

func addToFileInfo(parent *fileInfo, path string) (error, time.Time) {
	entries, err := os.ReadDir(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, time.Time{}
		}
		return err, time.Time{}
	}

	var latestTime time.Time
	for _, entry := range entries {
		// get existing file info
		var fi *fileInfo
		for _, ent := range parent.dirents {
			if ent.filename == entry.Name() {
				fi = ent
				break
			}
		}

		info, err := entry.Info()
		if err != nil {
			return err, time.Time{}
		}

		if latestTime.Before(info.ModTime()) {
			latestTime = info.ModTime()
		}

		// or create if not exist
		if fi == nil {
			fi = &fileInfo{
				filename: entry.Name(),
				mode:     info.Mode(),
			}
			parent.dirents = append(parent.dirents, fi)
		} else {
			// file overwrite is not supported -> return error
			if !entry.IsDir() || fi.fromHost != "" || fi.fromLiteral != "" {
				return fmt.Errorf("file already exists in filesystem: %s", filepath.Join(path, entry.Name())), time.Time{}
			}
		}

		// add content
		if entry.IsDir() {
			err, modTime := addToFileInfo(fi, filepath.Join(path, entry.Name()))
			if err != nil {
				return err, time.Time{}
			}
			if latestTime.Before(modTime) {
				latestTime = modTime
			}
		} else {
			fi.fromHost = filepath.Join(path, entry.Name())
		}
	}

	return nil, latestTime
}

func findExtraFiles() (map[string]*fileInfo, error) {
	buildPackages := buildPackagesFromFlags()
	extraFiles := make(map[string]*fileInfo, len(buildPackages))
	for pkg := range buildPackages {
		path := filepath.Join("extrafiles", pkg)

		fi := new(fileInfo)
		err, latestModTime := addToFileInfo(fi, path)
		if err != nil {
			return nil, err
		}
		if len(fi.dirents) == 0 {
			continue
		}

		packageConfigFiles[pkg] = append(packageConfigFiles[pkg], packageConfigFile{
			kind:         "include extra files in the root file system",
			path:         path,
			lastModified: latestModTime,
		})

		extraFiles[pkg] = fi
	}

	return extraFiles, nil
}

func findDontStart() (map[string]bool, error) {
	dontStartPaths, err := findPackageFiles("dontstart")
	if err != nil {
		return nil, err
	}

	if len(dontStartPaths) == 0 {
		return nil, nil // no dontstart.txt files found
	}

	buildPackages := buildPackagesFromFlags()

	contents := make(map[string]bool)
	for _, p := range dontStartPaths {
		pkg := strings.TrimSuffix(strings.TrimPrefix(p.path, "dontstart/"), "/dontstart.txt")
		if !buildPackages[pkg] {
			log.Printf("WARNING: environment variable file %s does not match any specified package (%s)", pkg, flag.Args())
			continue
		}
		packageConfigFiles[pkg] = append(packageConfigFiles[pkg], packageConfigFile{
			kind:         "not be started at boot",
			path:         p.path,
			lastModified: p.modTime,
		})

		// NOTE: ideally we would use the full package here, but our init
		// template only deals with base names right now.
		contents[filepath.Base(pkg)] = true
	}

	return contents, nil
}

type countingWriter int64

func (cw *countingWriter) Write(p []byte) (n int, err error) {
	*cw += countingWriter(len(p))
	return len(p), nil
}

func (p *pack) writeBootFile(bootfilename, mbrfilename string) error {
	f, err := os.Create(bootfilename)
	if err != nil {
		return err
	}
	defer f.Close()
	if err := p.writeBoot(f, mbrfilename); err != nil {
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

func (p *pack) overwriteDevice(dev string, root *fileInfo) error {
	if err := verifyNotMounted(dev); err != nil {
		return err
	}
	log.Printf("partitioning %s (GPT + Hybrid MBR)", dev)

	f, err := p.partition(*overwrite)
	if err != nil {
		return err
	}
	defer f.Close()

	if _, err := f.Seek(8192*512, io.SeekStart); err != nil {
		return err
	}

	if err := p.writeBoot(f, ""); err != nil {
		return err
	}

	if err := writeMBR(&offsetReadSeeker{f, 8192 * 512}, f, p.Partuuid); err != nil {
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
	partition := partitionPath(dev, "4")
	if p.ModifyCmdlineRoot() {
		partition = fmt.Sprintf("/dev/disk/by-partuuid/%s", p.PermUUID())
	} else {
		if target, err := filepath.EvalSymlinks(dev); err == nil {
			partition = partitionPath(target, "4")
		}
	}
	fmt.Printf("\tmkfs.ext4 %s\n", partition)
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

func (p *pack) overwriteFile(filename string, root *fileInfo) (bootSize int64, rootSize int64, err error) {
	f, err := os.Create(*overwrite)
	if err != nil {
		return 0, 0, err
	}

	if err := f.Truncate(int64(*targetStorageBytes)); err != nil {
		return 0, 0, err
	}

	if err := p.Partition(f, uint64(*targetStorageBytes)); err != nil {
		return 0, 0, err
	}

	if _, err := f.Seek(8192*512, io.SeekStart); err != nil {
		return 0, 0, err
	}
	var bs countingWriter
	if err := p.writeBoot(io.MultiWriter(f, &bs), ""); err != nil {
		return 0, 0, err
	}

	if err := writeMBR(&offsetReadSeeker{f, 8192 * 512}, f, p.Partuuid); err != nil {
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

	fmt.Printf("If your applications need to store persistent data, create a file system using e.g.:\n")
	fmt.Printf("\t/sbin/mkfs.ext4 %s -F -E offset=%v\n", *overwrite, 8192*512+1100*MB)
	fmt.Printf("\n")

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

type pack struct {
	packer.Pack
}

func filterGoEnv(env []string) []string {
	relevant := make([]string, 0, len(env))
	for _, kv := range env {
		if strings.HasPrefix(kv, "GOARCH=") ||
			strings.HasPrefix(kv, "GOOS=") ||
			strings.HasPrefix(kv, "CGO_ENABLED=") {
			relevant = append(relevant, kv)
		}
	}
	sort.Strings(relevant)
	return relevant
}

func logic() error {
	// TODO: go1.18 code to include git revision and git uncommitted status
	version := "<unknown>"
	info, ok := debug.ReadBuildInfo()
	if ok {
		version = fmt.Sprint(info.Main.Version)
	}
	fmt.Printf("gokrazy packer v%s on GOARCH=%s GOOS=%s\n\n", version, runtime.GOARCH, runtime.GOOS)

	// TODO: print the config directories which are used (host-specific config,
	// gokrazy config)

	// TODO: only if *update is yes:
	// TODO: fix update URL:
	fmt.Printf("Updating gokrazy installation on http://%s\n\n", *hostname)

	fmt.Printf("Build target: %s\n", strings.Join(filterGoEnv(packer.Env()), " "))

	buildTimestamp := time.Now().Format(time.RFC3339)
	fmt.Printf("Build timestamp: %s\n", buildTimestamp)

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

	systemCertsPEM, err := systemCertsPEM()
	if err != nil {
		return err
	}

	tmp, err := ioutil.TempDir("", "gokrazy-bins-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmp)

	packageBuildFlags, err := findBuildFlagsFiles()
	if err != nil {
		return err
	}

	flagFileContents, err := findFlagFiles()
	if err != nil {
		return err
	}

	envFileContents, err := findEnvFiles()
	if err != nil {
		return err
	}

	extraFiles, err := findExtraFiles()
	if err != nil {
		return err
	}

	dontStart, err := findDontStart()
	if err != nil {
		return err
	}

	args := flag.Args()
	fmt.Printf("Building %d Go packages:\n\n", len(args))
	for _, pkg := range args {
		fmt.Printf("  %s\n", pkg)
		for _, configFile := range packageConfigFiles[pkg] {
			fmt.Printf("    will %s\n",
				configFile.kind)
			fmt.Printf("      from %s\n",
				configFile.path)
			fmt.Printf("      last modified: %s (%s ago)\n",
				configFile.lastModified.Format(time.RFC3339),
				time.Since(configFile.lastModified).Round(1*time.Second))
		}
		fmt.Printf("\n")
	}

	pkgs := append(gokrazyPkgs, flag.Args()...)
	pkgs = append(pkgs, packer.InitDeps(*initPkg)...)
	noBuildPkgs := []string{
		*kernelPackage,
		*firmwarePackage,
		*eepromPackage,
	}
	if err := packer.Build(tmp, pkgs, packageBuildFlags, noBuildPkgs); err != nil {
		return err
	}

	root, err := findBins(tmp)
	if err != nil {
		return err
	}

	if *initPkg == "" {
		gokrazyInit := &gokrazyInit{
			root:             root,
			flagFileContents: flagFileContents,
			envFileContents:  envFileContents,
			buildTimestamp:   buildTimestamp,
			dontStart:        dontStart,
		}
		if *overwriteInit != "" {
			return gokrazyInit.dump(*overwriteInit)
		}

		tmpdir, err := gokrazyInit.build()
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

	defaultPassword, updateHostname := updateflag.GetUpdateTarget(*hostname)
	pw, err := ensurePasswordFileExists(updateHostname, defaultPassword)
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
		filename:    "ca-bundle.pem",
		fromLiteral: systemCertsPEM,
	})

	deployCertFile, deployKeyFile, err := getCertificate()
	if err != nil {
		return err
	}
	schema := "http"
	if deployCertFile != "" {
		// User requested TLS
		if *tlsInsecure {
			// If -insecure is specified, use http instead of https to make the
			// process of updating to non-empty -tls= a bit smoother.
		} else {
			schema = "https"
		}

		ssl.dirents = append(ssl.dirents, &fileInfo{
			filename: "gokrazy-web.pem",
			fromHost: deployCertFile,
		})
		ssl.dirents = append(ssl.dirents, &fileInfo{
			filename: "gokrazy-web.key.pem",
			fromHost: deployKeyFile,
		})
	}

	etc.dirents = append(etc.dirents, ssl)

	etc.dirents = append(etc.dirents, &fileInfo{
		filename:    "gokr-pw.txt",
		mode:        0400,
		fromLiteral: pw,
	})

	etc.dirents = append(etc.dirents, &fileInfo{
		filename:    "http-port.txt",
		fromLiteral: *httpPort,
	})

	etc.dirents = append(etc.dirents, &fileInfo{
		filename:    "https-port.txt",
		fromLiteral: *httpsPort,
	})

	for pkg1, fs1 := range extraFiles {
		// check against root fs
		if paths := getDuplication(root, fs1); len(paths) > 0 {
			return fmt.Errorf("extra files of package %s collides with root file system: %v", pkg1, paths)
		}

		// check against other packages
		for pkg2, fs2 := range extraFiles {
			if pkg1 == pkg2 {
				continue
			}

			if paths := getDuplication(fs1, fs2); len(paths) > 0 {
				return fmt.Errorf("extra files of package %s collides with package %s: %v", pkg1, pkg2, paths)
			}
		}

		// add extra files to rootfs
		if err := root.combine(fs1); err != nil {
			return fmt.Errorf("failed to add extra files from package %s: %v", pkg1, err)
		}
	}

	newInstallation := updateflag.NewInstallation()
	p := pack{
		Pack: packer.NewPackForHost(*hostname),
	}
	p.Pack.UsePartuuid = newInstallation
	p.Pack.UseGPTPartuuid = newInstallation
	var (
		updateHttpClient         *http.Client
		foundMatchingCertificate bool
		updateBaseUrl            *url.URL
		target                   *updater.Target
	)

	if !updateflag.NewInstallation() {
		updateBaseUrl, err = updateflag.BaseURL(*httpPort, schema, *hostname, pw)
		if err != nil {
			return err
		}

		updateHttpClient, foundMatchingCertificate, err = httpclient.GetTLSHttpClientByTLSFlag(useTLS, tlsInsecure, updateBaseUrl)
		if err != nil {
			return fmt.Errorf("getting http client by tls flag: %v", err)
		}
		done := measure.Interactively("probing https")
		remoteScheme, err := httpclient.GetRemoteScheme(updateBaseUrl)
		done("")
		if remoteScheme == "https" && !*tlsInsecure {
			updateBaseUrl.Scheme = "https"
			updateflag.SetUpdate(updateBaseUrl.String())
		}

		if updateBaseUrl.Scheme != "https" && foundMatchingCertificate {
			fmt.Printf("\n")
			fmt.Printf("!!!WARNING!!! Possible SSL-Stripping detected!\n")
			fmt.Printf("Found certificate for hostname in your client configuration but the host does not offer https!\n")
			fmt.Printf("\n")
			if !*tlsInsecure {
				log.Fatalf("update canceled: TLS certificate found, but negotiating a TLS connection with the target failed")
			}
			fmt.Printf("Proceeding anyway as requested (-insecure).\n")
		}

		// Opt out of PARTUUID= for updating until we can check the remote
		// userland version is new enough to understand how to set the active
		// root partition when PARTUUID= is in use.
		if err != nil {
			return err
		}
		updateBaseUrl.Path = "/"

		target, err = updater.NewTarget(updateBaseUrl.String(), updateHttpClient)
		if err != nil {
			return fmt.Errorf("checking target partuuid support: %v", err)
		}
		p.UsePartuuid = target.Supports("partuuid")
		p.UseGPTPartuuid = target.Supports("gpt")
	}
	fmt.Printf("\n")
	fmt.Printf("Feature summary:\n")
	fmt.Printf("  use PARTUUID: %v\n", p.UsePartuuid)
	fmt.Printf("  use GPT PARTUUID: %v\n", p.UseGPTPartuuid)

	// Determine where to write the boot and root images to.
	var (
		isDev                    bool
		tmpBoot, tmpRoot, tmpMBR *os.File
		bootSize, rootSize       int64
	)
	switch {
	case *overwrite != "":
		st, err := os.Stat(*overwrite)
		if err != nil && !os.IsNotExist(err) {
			return err
		}

		isDev = err == nil && st.Mode()&os.ModeDevice == os.ModeDevice

		if isDev {
			if err := p.overwriteDevice(*overwrite, root); err != nil {
				return err
			}
			fmt.Printf("To boot gokrazy, plug the SD card into a Raspberry Pi 3 or 4 (no other models supported)\n")
			fmt.Printf("\n")
		} else {
			lower := 1200*MB + 8192

			if *targetStorageBytes == 0 {
				return fmt.Errorf("-target_storage_bytes is required (e.g. -target_storage_bytes=%d) when using -overwrite with a file", lower)
			}
			if *targetStorageBytes%512 != 0 {
				return fmt.Errorf("-target_storage_bytes must be a multiple of 512 (sector size), use e.g. %d", lower)
			}
			if *targetStorageBytes < lower {
				return fmt.Errorf("-target_storage_bytes must be at least %d (for boot + 2 root file systems + 100 MB /perm)", lower)
			}

			bootSize, rootSize, err = p.overwriteFile(*overwrite, root)
			if err != nil {
				return err
			}

			fmt.Printf("To boot gokrazy, copy %s to an SD card and plug it into a Raspberry Pi 3 or 4 (no other models supported)\n", *overwrite)
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
			if err := p.writeBootFile(*overwriteBoot, mbrfn); err != nil {
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

			if err := p.writeBoot(tmpBoot, tmpMBR.Name()); err != nil {
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

	fmt.Printf("\nBuild complete!\n")

	hostPort := *hostname
	if schema == "http" && *httpPort != "80" {
		hostPort = fmt.Sprintf("%s:%s", hostPort, *httpPort)
	}
	if schema == "https" && *httpsPort != "443" {
		hostPort = fmt.Sprintf("%s:%s", hostPort, *httpsPort)
	}

	fmt.Printf("\nTo interact with the device, gokrazy provides a web interface reachable at:\n")
	fmt.Printf("\n")
	fmt.Printf("\t%s://gokrazy:%s@%s/\n", schema, pw, hostPort)
	fmt.Printf("\n")
	fmt.Printf("In addition, the following Linux consoles are set up:\n")
	fmt.Printf("\n")
	if *serialConsole != "disabled" {
		fmt.Printf("\t1. foreground Linux console on the serial port (115200n8, pin 6, 8, 10 for GND, TX, RX), accepting input\n")
		fmt.Printf("\t2. secondary Linux framebuffer console on HDMI; shows Linux kernel message but no init system messages\n")
	} else {
		fmt.Printf("\t1. foreground Linux framebuffer console on HDMI\n")
	}

	if *serialConsole != "disabled" {
		fmt.Printf("\n")
		fmt.Printf("Use -serial_console=disabled to make gokrazy not touch the serial port,\nand instead make the framebuffer console on HDMI the foreground console\n")
	}
	fmt.Printf("\n")
	if schema == "https" {
		certObj, err := getCertificateFromFile(deployCertFile)
		if err != nil {
			fmt.Errorf("error loading the certificate at %s", deployCertFile)
		} else {
			fmt.Printf("\n")
			fmt.Printf("The TLS Certificate of the gokrazy web interface is located under\n")
			fmt.Printf("\t%s\n", deployCertFile)
			fmt.Printf("The fingerprint of the Certificate is\n")
			fmt.Printf("\t%x\n", getCertificateFingerprintSHA1(certObj))
			fmt.Printf("The certificate is valid unitl\n")
			fmt.Printf("\t%s\n", certObj.NotAfter.String())
			fmt.Printf("Please verify the certificate, before adding an exception to your browser!\n")
		}
	}

	if err := <-dnsCheck; err != nil {
		fmt.Printf("\nWARNING: if the above URL does not work, perhaps name resolution (DNS) is broken\n")
		fmt.Printf("in your local network? Resolving your hostname failed: %v\n", err)
		fmt.Printf("Did you maybe configure a DNS server other than your router?\n\n")
	}

	if updateflag.NewInstallation() {
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

	updateBaseUrl.Path = "/"
	fmt.Printf("Updating %s\n", updateBaseUrl.String())

	progctx, canc := context.WithCancel(context.Background())
	defer canc()
	prog := &progress.Reporter{}
	go prog.Report(progctx)

	{
		start := time.Now()
		prog.SetStatus("update root file system")
		prog.SetTotal(0)
		if stater, ok := rootReader.(interface{ Stat() (os.FileInfo, error) }); ok {
			if st, err := stater.Stat(); err == nil {
				prog.SetTotal(uint64(st.Size()))
			}
		}
		// Start with the root file system because writing to the non-active
		// partition cannot break the currently running system.
		if err := target.StreamTo("root", io.TeeReader(rootReader, &progress.Writer{})); err != nil {
			return fmt.Errorf("updating root file system: %v", err)
		}
		duration := time.Since(start)
		transferred := progress.Reset()
		fmt.Printf("\rTransferred root file system (%s) at %.2f MiB/s (total: %v)\n",
			humanize.Bytes(transferred),
			float64(transferred)/duration.Seconds()/1024/1024,
			duration.Round(time.Second))
	}

	{
		start := time.Now()
		prog.SetStatus("update boot file system")
		prog.SetTotal(0)
		if stater, ok := bootReader.(interface{ Stat() (os.FileInfo, error) }); ok {
			if st, err := stater.Stat(); err == nil {
				prog.SetTotal(uint64(st.Size()))
			}
		}
		if err := target.StreamTo("boot", io.TeeReader(bootReader, &progress.Writer{})); err != nil {
			return fmt.Errorf("updating boot file system: %v", err)
		}
		duration := time.Since(start)
		transferred := progress.Reset()
		fmt.Printf("\rTransferred boot file system (%s) at %.2f MiB/s (total: %v)\n",
			humanize.Bytes(transferred),
			float64(transferred)/duration.Seconds()/1024/1024,
			duration.Round(time.Second))
	}

	if err := target.StreamTo("mbr", mbrReader); err != nil {
		if err == updater.ErrUpdateHandlerNotImplemented {
			log.Printf("target does not support updating MBR yet, ignoring")
		} else {
			return fmt.Errorf("updating MBR: %v", err)
		}
	}

	if *testboot {
		if err := target.Testboot(); err != nil {
			return fmt.Errorf("enable testboot of non-active partition: %v", err)
		}
	} else {
		if err := target.Switch(); err != nil {
			return fmt.Errorf("switching to non-active partition: %v", err)
		}
	}

	fmt.Printf("Triggering reboot\n")
	if err := target.Reboot(); err != nil {
		return fmt.Errorf("reboot: %v", err)
	}

	// Stop progress reporting to not mess up the following logs output.
	canc()

	const polltimeout = 5 * time.Minute
	fmt.Printf("Updated, waiting %v for the device to become reachable (cancel with Ctrl-C any time)\n", polltimeout)

	pollctx, canc := context.WithTimeout(context.Background(), polltimeout)
	defer canc()
	for {
		if err := pollctx.Err(); err != nil {
			return fmt.Errorf("device did not become healthy after update (%v)", err)
		}
		if err := pollUpdated1(pollctx, updateHttpClient, updateBaseUrl.String(), buildTimestamp); err != nil {
			log.Printf("device not yet reachable: %v", err)
			time.Sleep(1 * time.Second)
			continue
		}

		fmt.Printf("Device ready to use!\n")
		break
	}

	return nil
}

func main() {
	flag.Usage = func() {
		fmt.Fprintf(os.Stderr, usage)
		flag.PrintDefaults()
		os.Exit(2)
	}
	updateflag.RegisterFlags(flag.CommandLine)
	flag.Parse()

	gokrazyPkgs = strings.Split(*gokrazyPkgList, ",")

	if *overwrite == "" && *overwriteBoot == "" && *overwriteRoot == "" && *overwriteInit == "" && updateflag.NewInstallation() {
		flag.Usage()
	}

	if os.Getenv("GOKR_PACKER_FD") != "" { // partitioning child process
		p := pack{
			Pack: packer.NewPackForHost(*hostname),
		}

		if _, err := p.sudoPartition(*overwrite); err != nil {
			log.Fatal(err)
		}
		os.Exit(0)
	}

	if err := logic(); err != nil {
		log.Fatal(err)
	}
}
