// Package packer builds and deploys a gokrazy image. Called from the old
// gokr-packer binary and the new gok binary.
package packer

import (
	"archive/tar"
	"context"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/gokrazy/internal/config"
	"github.com/gokrazy/internal/deviceconfig"
	"github.com/gokrazy/tools/internal/log"
	"github.com/gokrazy/tools/packer"
)

type contextKey int

var BuildTimestampOverride contextKey

const MB = 1024 * 1024

type packageConfigFile struct {
	kind         string
	path         string
	lastModified time.Time
}

// packageConfigFiles is a map from package path to packageConfigFile, for constructing output that is keyed per package
var packageConfigFiles = make(map[string][]packageConfigFile)

func buildPackageMapFromFlags(cfg *config.Struct) map[string]bool {
	buildPackages := make(map[string]bool)
	for _, pkg := range cfg.Packages {
		buildPackages[pkg] = true
	}
	for _, pkg := range cfg.GokrazyPackagesOrDefault() {
		if strings.TrimSpace(pkg) == "" {
			continue
		}
		buildPackages[pkg] = true
	}
	return buildPackages
}

func buildPackagesFromFlags(cfg *config.Struct) []string {
	var buildPackages []string
	buildPackages = append(buildPackages, cfg.Packages...)
	buildPackages = append(buildPackages, getGokrazySystemPackages(cfg)...)
	return buildPackages
}

type archiveExtraction struct {
	dirs map[string]*FileInfo
}

func (ae *archiveExtraction) mkdirp(dir string) {
	if dir == "/" {
		// Special case to avoid strings.Split() returning a slice with the
		// empty string as only element, which would result in creating a
		// subdirectory of the root directory without a name.
		return
	}
	parts := strings.Split(strings.TrimPrefix(dir, "/"), "/")
	parent := ae.dirs["."]
	for idx, part := range parts {
		path := strings.Join(parts[:1+idx], "/")
		if dir, ok := ae.dirs[path]; ok {
			parent = dir
			continue
		}
		subdir := &FileInfo{
			Filename: part,
		}
		parent.Dirents = append(parent.Dirents, subdir)
		ae.dirs[path] = subdir
		parent = subdir
	}
}

func (ae *archiveExtraction) extractArchive(path string) (time.Time, error) {
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return time.Time{}, nil
		}
		return time.Time{}, err
	}
	defer f.Close()
	rd := tar.NewReader(f)

	var latestTime time.Time
	for {
		header, err := rd.Next()
		if err != nil {
			if err == io.EOF {
				break
			}
			return time.Time{}, err
		}

		// header.Name is e.g. usr/lib/aarch64-linux-gnu/xtables/libebt_mark.so
		// for files, but e.g. usr/lib/ (note the trailing /) for directories.
		filename := strings.TrimSuffix(header.Name, "/")

		fi := &FileInfo{
			Filename: filepath.Base(filename),
			Mode:     os.FileMode(header.Mode),
		}

		if latestTime.Before(header.ModTime) {
			latestTime = header.ModTime
		}

		dir := filepath.Dir(filename)
		// Create all directory elements. Archives can contain directory entries
		// without having entries for their parent, e.g. web/assets/fonts/ might
		// be the first entry in an archive.
		ae.mkdirp(dir)
		parent := ae.dirs[dir]
		parent.Dirents = append(parent.Dirents, fi)

		switch header.Typeflag {
		case tar.TypeSymlink:
			fi.SymlinkDest = header.Linkname

		case tar.TypeDir:
			ae.dirs[filename] = fi

		default:
			// TODO(optimization): do not hold file data in memory, instead
			// stream the archive contents lazily to conserve RAM
			b, err := io.ReadAll(rd)
			if err != nil {
				return time.Time{}, err
			}
			fi.FromLiteral = string(b)
		}
	}

	return latestTime, nil
}

// findExtraFilesInDir probes for extrafiles .tar files (possibly with an
// architecture suffix like _amd64), or whether dir itself exists.
func findExtraFilesInDir(dir string) (string, error) {
	targetArch := packer.TargetArch()

	var err error
	for _, p := range []string{
		dir + "_" + targetArch + ".tar",
		dir + ".tar",
		dir,
	} {
		_, err = os.Stat(p)
		if err == nil {
			return p, nil
		}
		if !os.IsNotExist(err) {
			return "", err
		}
	}
	return "", err // return last error
}

// TODO(cleanup): It would be nice to de-duplicate the path resolution logic
// between findExtraFilesInDir and addExtraFilesFromDir. Maybe
// findExtraFilesInDir could os.Open the file and pass the file handle to the
// caller. That would prevent any TOCTOU problems.
func addExtraFilesFromDir(pkg, dir string, fi *FileInfo) error {
	ae := archiveExtraction{
		dirs: make(map[string]*FileInfo),
	}
	ae.dirs["."] = fi // root

	targetArch := packer.TargetArch()

	effectivePath := dir + "_" + targetArch + ".tar"
	latestModTime, err := ae.extractArchive(effectivePath)
	if err != nil {
		return err
	}
	if len(fi.Dirents) == 0 {
		effectivePath = dir + ".tar"
		latestModTime, err = ae.extractArchive(effectivePath)
		if err != nil {
			return err
		}
	}
	if len(fi.Dirents) == 0 {
		effectivePath = dir
		latestModTime, err = addToFileInfo(fi, effectivePath)
		if err != nil {
			return err
		}
		if len(fi.Dirents) == 0 {
			return nil
		}
	}

	packageConfigFiles[pkg] = append(packageConfigFiles[pkg], packageConfigFile{
		kind:         "include extra files in the root file system",
		path:         effectivePath,
		lastModified: latestModTime,
	})

	return nil
}

func mkdirp(root *FileInfo, dir string) *FileInfo {
	if dir == "/" {
		// Special case to avoid strings.Split() returning a slice with the
		// empty string as only element, which would result in creating a
		// subdirectory of the root directory without a name.
		return root
	}
	parts := strings.Split(strings.TrimPrefix(dir, "/"), "/")
	parent := root
	for _, part := range parts {
		subdir := &FileInfo{
			Filename: part,
		}
		parent.Dirents = append(parent.Dirents, subdir)
		parent = subdir
	}
	return parent
}

func FindExtraFiles(cfg *config.Struct) (map[string][]*FileInfo, error) {
	extraFiles := make(map[string][]*FileInfo)
	if len(cfg.PackageConfig) > 0 {
		for pkg, packageConfig := range cfg.PackageConfig {
			var fileInfos []*FileInfo

			for dest, path := range packageConfig.ExtraFilePaths {
				root := &FileInfo{}
				if st, err := os.Stat(path); err == nil && st.Mode().IsRegular() {
					var err error
					path, err = filepath.Abs(path)
					if err != nil {
						return nil, err
					}
					// Copy a file from the host
					dir := mkdirp(root, filepath.Dir(dest))
					dir.Dirents = append(dir.Dirents, &FileInfo{
						Filename: filepath.Base(dest),
						FromHost: path,
					})
					packageConfigFiles[pkg] = append(packageConfigFiles[pkg], packageConfigFile{
						kind:         "include extra files in the root file system",
						path:         path,
						lastModified: st.ModTime(),
					})
				} else {
					// Check if the ExtraFilePaths entry refers to an extrafiles
					// .tar archive or an existing directory. If nothing can be
					// found, report the error so the user can fix their config.
					_, err := findExtraFilesInDir(path)
					if err != nil {
						return nil, fmt.Errorf("ExtraFilePaths of %s: %v", pkg, err)
					}
					// Copy a tarball or directory from the host
					dir := mkdirp(root, dest)
					if err := addExtraFilesFromDir(pkg, path, dir); err != nil {
						return nil, err
					}
				}

				fileInfos = append(fileInfos, root)
			}

			for dest, contents := range packageConfig.ExtraFileContents {
				root := &FileInfo{}
				dir := mkdirp(root, filepath.Dir(dest))
				dir.Dirents = append(dir.Dirents, &FileInfo{
					Filename:    filepath.Base(dest),
					FromLiteral: contents,
				})
				packageConfigFiles[pkg] = append(packageConfigFiles[pkg], packageConfigFile{
					kind: "include extra files in the root file system",
				})
				fileInfos = append(fileInfos, root)
			}

			extraFiles[pkg] = fileInfos
		}
		// fall through to look for extra files in <pkg>/_gokrazy/extrafiles
	}

	buildPackages := buildPackagesFromFlags(cfg)
	packageDirs, err := packer.PackageDirs(buildPackages)
	if err != nil {
		return nil, err
	}
	for idx, pkg := range buildPackages {
		if len(cfg.PackageConfig) == 0 {
			// Look for extra files in $PWD/extrafiles/<pkg>/
			dir := filepath.Join("extrafiles", pkg)
			root := &FileInfo{}
			if err := addExtraFilesFromDir(pkg, dir, root); err != nil {
				return nil, err
			}
			extraFiles[pkg] = append(extraFiles[pkg], root)
		}
		{
			// Look for extra files in <pkg>/_gokrazy/extrafiles/
			dir := packageDirs[idx]
			subdir := filepath.Join(dir, "_gokrazy", "extrafiles")
			root := &FileInfo{}
			if err := addExtraFilesFromDir(pkg, subdir, root); err != nil {
				return nil, err
			}
			extraFiles[pkg] = append(extraFiles[pkg], root)
		}
	}

	return extraFiles, nil
}

func verifyNotMounted(dev string) error {
	b, err := os.ReadFile("/proc/self/mountinfo")
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
			return fmt.Errorf("partition %s is mounted on %s", parts[9], parts[4])
		}
	}
	return nil
}

type OutputType string

const (
	OutputTypeGaf  OutputType = "gaf"
	OutputTypeFull OutputType = "full"
)

type OutputStruct struct {
	Path string     `json:",omitempty"`
	Type OutputType `json:",omitempty"`
}

type Osenv struct {
	Stdin  io.Reader
	Stdout io.Writer
	Stderr io.Writer
	logger log.Logger
}

func (s *Osenv) initLogger() {
	if s.logger == nil {
		s.logger = log.New(s.Stderr)
	}
}

func (s *Osenv) Logger() log.Logger {
	s.initLogger()
	return s.logger
}

func (s *Osenv) Logf(format string, v ...any) {
	s.initLogger()
	s.logger.Printf(format, v...)
}

type Pack struct {
	packer.Pack

	// Everything Operating System environment related
	// like input/output channels to use (for logging).
	Env Osenv

	// FileCfg holds an untouched copy
	// of the config file, as it was read from disk.
	FileCfg *config.Struct
	Cfg     *config.Struct
	Output  *OutputStruct

	// state
	buildTimestamp              string
	rootDeviceFiles             []deviceconfig.RootFile
	firstPartitionOffsetSectors int64
	systemCertsPEM              string
	packageBuildFlags           map[string][]string
	packageBuildTags            map[string][]string
	packageBuildEnv             map[string][]string
	flagFileContents            map[string][]string
	envFileContents             map[string][]string
	dontStart                   map[string]bool
	waitForClock                map[string]bool
	waitFor                     map[string][]string
	basenames                   map[string]string
	schema                      string
	update                      *config.UpdateStruct
	root                        *FileInfo
	sbom                        []byte
	sbomWithHash                SBOMWithHash
	initTmp                     string
	kernelDir                   string
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

const programName = "gokrazy gok"

func (pack *Pack) logic(ctx context.Context) error {
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

	if err := pack.logicPrepare(ctx); err != nil {
		return err
	}

	bindir, err := os.MkdirTemp("", "gokrazy-bins-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(bindir)

	if err := pack.logicBuild(bindir); err != nil {
		return err
	}
	defer os.RemoveAll(pack.initTmp)

	if err := pack.logicWrite(dnsCheck); err != nil {
		return err
	}

	return nil
}

func (pack *Pack) Main(ctx context.Context) {
	if err := pack.logic(ctx); err != nil {
		fmt.Fprintf(os.Stderr, "ERROR:\n  %s\n", err)
		os.Exit(1)
	}
}

func (pack *Pack) GenerateSBOM(ctx context.Context) ([]byte, SBOMWithHash, error) {
	if err := pack.logicPrepare(ctx); err != nil {
		return nil, SBOMWithHash{}, err
	}

	bindir, err := os.MkdirTemp("", "gokrazy-bins-")
	if err != nil {
		return nil, SBOMWithHash{}, err
	}
	defer os.RemoveAll(bindir)

	if err := pack.logicBuild(bindir); err != nil {
		return nil, SBOMWithHash{}, err
	}
	defer os.RemoveAll(pack.initTmp)
	return pack.sbom, pack.sbomWithHash, nil
}
