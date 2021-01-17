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
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	// Imported so that the go tool will download the repositories
	_ "github.com/gokrazy/gokrazy/empty"

	"github.com/gokrazy/internal/httpclient"
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

	update = flag.String("update",
		os.Getenv("GOKRAZY_UPDATE"),
		`URL of a gokrazy installation (e.g. http://gokrazy:mypassword@myhostname/) to update. The special value "yes" uses the stored password and -hostname value to construct the URL`)

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

type filePathAndModTime struct {
	path    string
	modTime time.Time
}

func (f *filePathAndModTime) lastModified() string {
	return fmt.Sprintf("%s (%s ago)", f.modTime.Format(time.RFC3339), time.Since(f.modTime).Round(1*time.Second))
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
	lastModified string
}

// packageConfigFiles is a map from package path to packageConfigFile, for constructing output that is keyed per package
var packageConfigFiles = make(map[string][]packageConfigFile)

func findFlagFiles() (map[string]string, error) {
	flagFilePaths, err := findPackageFiles("flags")
	if err != nil {
		return nil, err
	}

	if len(flagFilePaths) == 0 {
		return nil, nil // no flags.txt files found
	}

	buildPackages := make(map[string]bool)
	for _, pkg := range flag.Args() {
		buildPackages[pkg] = true
	}

	contents := make(map[string]string)
	for _, p := range flagFilePaths {
		pkg := strings.TrimSuffix(strings.TrimPrefix(p.path, "flags/"), "/flags.txt")
		if !buildPackages[pkg] {
			log.Printf("WARNING: flag file %s does not match any specified package (%s)", pkg, flag.Args())
			continue
		}
		packageConfigFiles[pkg] = append(packageConfigFiles[pkg], packageConfigFile{
			kind:         "started with command-line flags",
			path:         p.path,
			lastModified: p.lastModified(),
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

func findBuildFlagsFiles() (map[string]string, error) {
	buildFlagsFilePaths, err := findPackageFiles("buildflags")
	if err != nil {
		return nil, err
	}

	if len(buildFlagsFilePaths) == 0 {
		return nil, nil // no flags.txt files found
	}

	buildPackages := make(map[string]bool)
	for _, pkg := range flag.Args() {
		buildPackages[pkg] = true
	}

	contents := make(map[string]string)
	for _, p := range buildFlagsFilePaths {
		pkg := strings.TrimSuffix(strings.TrimPrefix(p.path, "buildflags/"), "/buildflags.txt")
		if !buildPackages[pkg] {
			log.Printf("WARNING: buildflags file %s does not match any specified package (%s)", pkg, flag.Args())
			continue
		}
		packageConfigFiles[pkg] = append(packageConfigFiles[pkg], packageConfigFile{
			kind:         "compiled with build flags",
			path:         p.path,
			lastModified: p.lastModified(),
		})

		b, err := ioutil.ReadFile(p.path)
		if err != nil {
			return nil, err
		}

		// use full package path opposed to flags
		contents[pkg] = strings.TrimSpace(string(b))
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

	buildPackages := make(map[string]bool)
	for _, pkg := range flag.Args() {
		buildPackages[pkg] = true
	}

	contents := make(map[string]string)
	for _, p := range buildFlagsFilePaths {
		pkg := strings.TrimSuffix(strings.TrimPrefix(p.path, "env/"), "/env.txt")
		if !buildPackages[pkg] {
			log.Printf("WARNING: environment variable file %s does not match any specified package (%s)", pkg, flag.Args())
			continue
		}
		packageConfigFiles[pkg] = append(packageConfigFiles[pkg], packageConfigFile{
			kind:         "started with environment variables",
			path:         p.path,
			lastModified: p.lastModified(),
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

type countingWriter int64

func (cw *countingWriter) Write(p []byte) (n int, err error) {
	*cw += countingWriter(len(p))
	return len(p), nil
}

func writeBootFile(bootfilename, mbrfilename string, partuuid uint32, usePartuuid bool) error {
	f, err := os.Create(bootfilename)
	if err != nil {
		return err
	}
	defer f.Close()
	if err := writeBoot(f, mbrfilename, partuuid, usePartuuid); err != nil {
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

func overwriteDevice(dev string, root *fileInfo, partuuid uint32, usePartuuid bool) error {
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

	if err := writeBoot(f, "", partuuid, usePartuuid); err != nil {
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
	partition := partitionPath(dev, "4")
	if usePartuuid {
		partition = fmt.Sprintf("/dev/disk/by-partuuid/%08x-04", partuuid)
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

func overwriteFile(filename string, root *fileInfo, partuuid uint32, usePartuuid bool) (bootSize int64, rootSize int64, err error) {
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
	if err := writeBoot(io.MultiWriter(f, &bs), "", partuuid, usePartuuid); err != nil {
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

	log.Printf("building %v", flag.Args())

	tmp, err := ioutil.TempDir("", "gokrazy-bins-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmp)

	packageBuildFlags, err := findBuildFlagsFiles()
	if err != nil {
		return err
	}

	if err := build(tmp, packageBuildFlags); err != nil {
		return err
	}

	root, err := findBins(tmp)
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

	for pkg, configFiles := range packageConfigFiles {
		log.Printf("package %s:", pkg)
		for _, configFile := range configFiles {
			log.Printf("  will be %s",
				configFile.kind)
			log.Printf("    from %s",
				configFile.path)
			log.Printf("    last modified: %s",
				configFile.lastModified)

		}
		log.Printf("")
	}

	if *initPkg == "" {
		gokrazyInit := &gokrazyInit{
			root:             root,
			flagFileContents: flagFileContents,
			envFileContents:  envFileContents,
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

	var defaultPassword string
	updateHostname := *hostname
	if *update != "" && *update != "yes" {
		if u, err := url.Parse(*update); err == nil {
			defaultPassword, _ = u.User.Password()
			updateHostname = u.Host
		}
	}
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
		filename: "ca-bundle.pem",
		fromHost: cacerts,
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

	if *update == "yes" {
		*update = schema + "://gokrazy:" + pw + "@" + *hostname + "/"
	}

	partuuid := derivePartUUID(*hostname)
	usePartuuid := true
	var (
		updateHttpClient         *http.Client
		foundMatchingCertificate bool
		updateBaseUrl            *url.URL
		target                   *updater.Target
	)

	if *update != "" {
		updateBaseUrl, err = url.Parse(*update)
		if err != nil {
			return err
		}

		updateHttpClient, foundMatchingCertificate, err = httpclient.GetTLSHttpClientByTLSFlag(useTLS, tlsInsecure, updateBaseUrl)
		if err != nil {
			return fmt.Errorf("getting http client by tls flag: %v", err)
		}
		remoteScheme, err := httpclient.GetRemoteScheme(updateBaseUrl)
		if remoteScheme == "https" && !*tlsInsecure {
			updateBaseUrl.Scheme = "https"
			*update = updateBaseUrl.String()
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
		usePartuuid := target.Supports("partuuid")
		log.Printf("target partuuid support: %v", usePartuuid)
	}

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
			if err := overwriteDevice(*overwrite, root, partuuid, usePartuuid); err != nil {
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

			bootSize, rootSize, err = overwriteFile(*overwrite, root, partuuid, usePartuuid)
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
			if err := writeBootFile(*overwriteBoot, mbrfn, partuuid, usePartuuid); err != nil {
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

			if err := writeBoot(tmpBoot, tmpMBR.Name(), partuuid, usePartuuid); err != nil {
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

	hostPort := *hostname
	if schema == "http" && *httpPort != "80" {
		hostPort = fmt.Sprintf("%s:%s", hostPort, *httpPort)
	}
	if schema == "https" && *httpsPort != "443" {
		hostPort = fmt.Sprintf("%s:%s", hostPort, *httpsPort)
	}

	fmt.Printf("To interact with the device, gokrazy provides a web interface reachable at:\n")
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

	updateBaseUrl.Path = "/"
	log.Printf("Updating %q", *update)

	{
		start := time.Now()
		var cw countingWriter
		// Start with the root file system because writing to the non-active
		// partition cannot break the currently running system.
		if err := target.StreamTo("root", io.TeeReader(rootReader, &cw)); err != nil {
			return fmt.Errorf("updating root file system: %v", err)
		}
		duration := time.Since(start)
		log.Printf("root update done: %d bytes in %v, i.e. %.2f MiB/s", int64(cw), duration, float64(cw)/duration.Seconds()/1024/1024)
	}

	{
		start := time.Now()
		var cw countingWriter
		if err := target.StreamTo("boot", io.TeeReader(bootReader, &cw)); err != nil {
			return fmt.Errorf("updating boot file system: %v", err)
		}
		duration := time.Since(start)
		log.Printf("boot update done: %d bytes in %v, i.e. %.2f MiB/s", int64(cw), duration, float64(cw)/duration.Seconds()/1024/1024)
	}

	if err := target.StreamTo("mbr", mbrReader); err != nil {
		if err == updater.ErrUpdateHandlerNotImplemented {
			log.Printf("target does not support updating MBR yet, ignoring")
		} else {
			return fmt.Errorf("updating MBR: %v", err)
		}
	}

	if err := target.Switch(); err != nil {
		return fmt.Errorf("switching to non-active partition: %v", err)
	}

	log.Printf("triggering reboot")
	if err := target.Reboot(); err != nil {
		return fmt.Errorf("reboot: %v", err)
	}

	log.Printf("updated, should be back within 10 to 30 seconds")
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
