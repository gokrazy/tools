// Package packer builds and deploys a gokrazy image. Called from the old
// gokr-packer binary and the new gok binary.
package packer

import (
	"archive/tar"
	"bufio"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"runtime/trace"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/gokrazy/internal/config"
	"github.com/gokrazy/internal/deviceconfig"
	"github.com/gokrazy/internal/httpclient"
	"github.com/gokrazy/internal/humanize"
	"github.com/gokrazy/internal/progress"
	"github.com/gokrazy/internal/tlsflag"
	"github.com/gokrazy/internal/updateflag"
	"github.com/gokrazy/tools/internal/log"
	"github.com/gokrazy/tools/internal/measure"
	"github.com/gokrazy/tools/internal/version"
	"github.com/gokrazy/tools/packer"
	"github.com/gokrazy/updater"
)

type contextKey int

var BuildTimestampOverride contextKey

const MB = 1024 * 1024

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

func (pack *Pack) findFlagFiles(cfg *config.Struct) (map[string][]string, error) {
	log := pack.Env.Logger()

	if len(cfg.PackageConfig) > 0 {
		contents := make(map[string][]string)
		for pkg, packageConfig := range cfg.PackageConfig {
			if len(packageConfig.CommandLineFlags) == 0 {
				continue
			}
			contents[pkg] = packageConfig.CommandLineFlags
			packageConfigFiles[pkg] = append(packageConfigFiles[pkg], packageConfigFile{
				kind:         "be started with command-line flags",
				path:         cfg.Meta.Path,
				lastModified: cfg.Meta.LastModified,
			})
		}
		return contents, nil
	}

	flagFilePaths, err := findPackageFiles("flags")
	if err != nil {
		return nil, err
	}

	if len(flagFilePaths) == 0 {
		return nil, nil // no flags.txt files found
	}

	buildPackages := buildPackageMapFromFlags(cfg)

	contents := make(map[string][]string)
	for _, p := range flagFilePaths {
		pkg := strings.TrimSuffix(strings.TrimPrefix(p.path, "flags/"), "/flags.txt")
		if !buildPackages[pkg] {
			log.Printf("WARNING: flag file %s does not match any specified package (%s)", pkg, cfg.Packages)
			continue
		}
		packageConfigFiles[pkg] = append(packageConfigFiles[pkg], packageConfigFile{
			kind:         "be started with command-line flags",
			path:         p.path,
			lastModified: p.modTime,
		})

		b, err := os.ReadFile(p.path)
		if err != nil {
			return nil, err
		}
		lines := strings.Split(strings.TrimSpace(string(b)), "\n")
		contents[pkg] = lines
	}

	return contents, nil
}

func (pack *Pack) findBuildFlagsFiles(cfg *config.Struct) (map[string][]string, error) {
	log := pack.Env.Logger()

	if len(cfg.PackageConfig) > 0 {
		contents := make(map[string][]string)
		for pkg, packageConfig := range cfg.PackageConfig {
			if len(packageConfig.GoBuildFlags) == 0 {
				continue
			}
			contents[pkg] = packageConfig.GoBuildFlags
			packageConfigFiles[pkg] = append(packageConfigFiles[pkg], packageConfigFile{
				kind:         "be compiled with build flags",
				path:         cfg.Meta.Path,
				lastModified: cfg.Meta.LastModified,
			})
		}
		return contents, nil
	}

	buildFlagsFilePaths, err := findPackageFiles("buildflags")
	if err != nil {
		return nil, err
	}

	if len(buildFlagsFilePaths) == 0 {
		return nil, nil // no flags.txt files found
	}

	buildPackages := buildPackageMapFromFlags(cfg)

	contents := make(map[string][]string)
	for _, p := range buildFlagsFilePaths {
		pkg := strings.TrimSuffix(strings.TrimPrefix(p.path, "buildflags/"), "/buildflags.txt")
		if !buildPackages[pkg] {
			log.Printf("WARNING: buildflags file %s does not match any specified package (%s)", pkg, cfg.Packages)
			continue
		}
		packageConfigFiles[pkg] = append(packageConfigFiles[pkg], packageConfigFile{
			kind:         "be compiled with build flags",
			path:         p.path,
			lastModified: p.modTime,
		})

		b, err := os.ReadFile(p.path)
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

func findBuildEnv(cfg *config.Struct) (map[string][]string, error) {
	contents := make(map[string][]string)
	for pkg, packageConfig := range cfg.PackageConfig {
		if len(packageConfig.GoBuildEnvironment) == 0 {
			continue
		}
		contents[pkg] = packageConfig.GoBuildEnvironment
		packageConfigFiles[pkg] = append(packageConfigFiles[pkg], packageConfigFile{
			kind:         "be compiled with build environment variables",
			path:         cfg.Meta.Path,
			lastModified: cfg.Meta.LastModified,
		})
	}
	return contents, nil
}

func (pack *Pack) findBuildTagsFiles(cfg *config.Struct) (map[string][]string, error) {
	log := pack.Env.Logger()

	if len(cfg.PackageConfig) > 0 {
		contents := make(map[string][]string)
		for pkg, packageConfig := range cfg.PackageConfig {
			if len(packageConfig.GoBuildTags) == 0 {
				continue
			}
			contents[pkg] = packageConfig.GoBuildTags
			packageConfigFiles[pkg] = append(packageConfigFiles[pkg], packageConfigFile{
				kind:         "be compiled with build tags",
				path:         cfg.Meta.Path,
				lastModified: cfg.Meta.LastModified,
			})
		}
		return contents, nil
	}

	buildTagsFiles, err := findPackageFiles("buildtags")
	if err != nil {
		return nil, err
	}

	if len(buildTagsFiles) == 0 {
		return nil, nil // no flags.txt files found
	}

	buildPackages := buildPackageMapFromFlags(cfg)

	contents := make(map[string][]string)
	for _, p := range buildTagsFiles {
		pkg := strings.TrimSuffix(strings.TrimPrefix(p.path, "buildtags/"), "/buildtags.txt")
		if !buildPackages[pkg] {
			log.Printf("WARNING: buildtags file %s does not match any specified package (%s)", pkg, cfg.Packages)
			continue
		}
		packageConfigFiles[pkg] = append(packageConfigFiles[pkg], packageConfigFile{
			kind:         "be compiled with build tags",
			path:         p.path,
			lastModified: p.modTime,
		})

		b, err := os.ReadFile(p.path)
		if err != nil {
			return nil, err
		}

		var buildTags []string
		sc := bufio.NewScanner(strings.NewReader(string(b)))
		for sc.Scan() {
			if flag := sc.Text(); flag != "" {
				buildTags = append(buildTags, flag)
			}
		}

		if err := sc.Err(); err != nil {
			return nil, err
		}

		// use full package path opposed to flags
		contents[pkg] = buildTags
	}

	return contents, nil
}

func (pack *Pack) findEnvFiles(cfg *config.Struct) (map[string][]string, error) {
	log := pack.Env.Logger()

	if len(cfg.PackageConfig) > 0 {
		contents := make(map[string][]string)
		for pkg, packageConfig := range cfg.PackageConfig {
			if len(packageConfig.Environment) == 0 {
				continue
			}
			contents[pkg] = packageConfig.Environment
			packageConfigFiles[pkg] = append(packageConfigFiles[pkg], packageConfigFile{
				kind:         "be started with environment variables",
				path:         cfg.Meta.Path,
				lastModified: cfg.Meta.LastModified,
			})
		}
		return contents, nil
	}

	buildFlagsFilePaths, err := findPackageFiles("env")
	if err != nil {
		return nil, err
	}

	if len(buildFlagsFilePaths) == 0 {
		return nil, nil // no flags.txt files found
	}

	buildPackages := buildPackageMapFromFlags(cfg)

	contents := make(map[string][]string)
	for _, p := range buildFlagsFilePaths {
		pkg := strings.TrimSuffix(strings.TrimPrefix(p.path, "env/"), "/env.txt")
		if !buildPackages[pkg] {
			log.Printf("WARNING: environment variable file %s does not match any specified package (%s)", pkg, cfg.Packages)
			continue
		}
		packageConfigFiles[pkg] = append(packageConfigFiles[pkg], packageConfigFile{
			kind:         "be started with environment variables",
			path:         p.path,
			lastModified: p.modTime,
		})

		b, err := os.ReadFile(p.path)
		if err != nil {
			return nil, err
		}
		lines := strings.Split(strings.TrimSpace(string(b)), "\n")
		contents[pkg] = lines
	}

	return contents, nil
}

func addToFileInfo(parent *FileInfo, path string) (time.Time, error) {
	entries, err := os.ReadDir(path)
	if err != nil {
		if os.IsNotExist(err) {
			return time.Time{}, nil
		}
		return time.Time{}, err
	}

	var latestTime time.Time
	for _, entry := range entries {
		filename := entry.Name()
		// get existing file info
		var fi *FileInfo
		for _, ent := range parent.Dirents {
			if ent.Filename == filename {
				fi = ent
				break
			}
		}

		info, err := entry.Info()
		if err != nil {
			return time.Time{}, err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			info, err = os.Stat(filepath.Join(path, filename))
			if err != nil {
				return time.Time{}, err
			}
		}

		if latestTime.Before(info.ModTime()) {
			latestTime = info.ModTime()
		}

		// or create if not exist
		if fi == nil {
			fi = &FileInfo{
				Filename: filename,
				Mode:     info.Mode(),
			}
			parent.Dirents = append(parent.Dirents, fi)
		} else {
			// file overwrite is not supported -> return error
			if !info.IsDir() || fi.FromHost != "" || fi.FromLiteral != "" {
				return time.Time{}, fmt.Errorf("file already exists in filesystem: %s", filepath.Join(path, filename))
			}
		}

		// add content
		if info.IsDir() {
			modTime, err := addToFileInfo(fi, filepath.Join(path, filename))
			if err != nil {
				return time.Time{}, err
			}
			if latestTime.Before(modTime) {
				latestTime = modTime
			}
		} else {
			fi.FromHost = filepath.Join(path, filename)
		}
	}

	return latestTime, nil
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

func (pack *Pack) findDontStart(cfg *config.Struct) (map[string]bool, error) {
	log := pack.Env.Logger()

	if len(cfg.PackageConfig) > 0 {
		contents := make(map[string]bool)
		for pkg, packageConfig := range cfg.PackageConfig {
			if !packageConfig.DontStart {
				continue
			}
			contents[pkg] = packageConfig.DontStart
			packageConfigFiles[pkg] = append(packageConfigFiles[pkg], packageConfigFile{
				kind:         "not be started at boot",
				path:         cfg.Meta.Path,
				lastModified: cfg.Meta.LastModified,
			})
		}
		return contents, nil
	}

	dontStartPaths, err := findPackageFiles("dontstart")
	if err != nil {
		return nil, err
	}

	if len(dontStartPaths) == 0 {
		return nil, nil // no dontstart.txt files found
	}

	buildPackages := buildPackageMapFromFlags(cfg)

	contents := make(map[string]bool)
	for _, p := range dontStartPaths {
		pkg := strings.TrimSuffix(strings.TrimPrefix(p.path, "dontstart/"), "/dontstart.txt")
		if !buildPackages[pkg] {
			log.Printf("WARNING: dontstart.txt file %s does not match any specified package (%s)", pkg, cfg.Packages)
			continue
		}
		packageConfigFiles[pkg] = append(packageConfigFiles[pkg], packageConfigFile{
			kind:         "not be started at boot",
			path:         p.path,
			lastModified: p.modTime,
		})

		contents[pkg] = true
	}

	return contents, nil
}

func (pack *Pack) findWaitForClock(cfg *config.Struct) (map[string]bool, error) {
	log := pack.Env.Logger()

	if len(cfg.PackageConfig) > 0 {
		contents := make(map[string]bool)
		for pkg, packageConfig := range cfg.PackageConfig {
			if !packageConfig.WaitForClock {
				continue
			}
			contents[pkg] = packageConfig.WaitForClock
			packageConfigFiles[pkg] = append(packageConfigFiles[pkg], packageConfigFile{
				kind:         "wait for clock synchronization before start",
				path:         cfg.Meta.Path,
				lastModified: cfg.Meta.LastModified,
			})
		}
		return contents, nil
	}

	waitForClockPaths, err := findPackageFiles("waitforclock")
	if err != nil {
		return nil, err
	}

	if len(waitForClockPaths) == 0 {
		return nil, nil // no waitforclock.txt files found
	}

	buildPackages := buildPackageMapFromFlags(cfg)

	contents := make(map[string]bool)
	for _, p := range waitForClockPaths {
		pkg := strings.TrimSuffix(strings.TrimPrefix(p.path, "waitforclock/"), "/waitforclock.txt")
		if !buildPackages[pkg] {
			log.Printf("WARNING: waitforclock.txt file %s does not match any specified package (%s)", pkg, cfg.Packages)
			continue
		}
		packageConfigFiles[pkg] = append(packageConfigFiles[pkg], packageConfigFile{
			kind:         "wait for clock synchronization before start",
			path:         p.path,
			lastModified: p.modTime,
		})

		contents[pkg] = true
	}

	return contents, nil
}

func findBasenames(cfg *config.Struct) (map[string]string, error) {
	contents := make(map[string]string)
	for pkg, packageConfig := range cfg.PackageConfig {
		if packageConfig.Basename == "" {
			continue
		}
		contents[pkg] = packageConfig.Basename
		packageConfigFiles[pkg] = append(packageConfigFiles[pkg], packageConfigFile{
			kind: "be installed with the basename set to " + packageConfig.Basename,
		})
	}
	return contents, nil
}

type countingWriter int64

func (cw *countingWriter) Write(p []byte) (n int, err error) {
	*cw += countingWriter(len(p))
	return len(p), nil
}

func (p *Pack) writeBootFile(bootfilename, mbrfilename string) error {
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

func (p *Pack) writeRootFile(filename string, root *FileInfo) error {
	f, err := os.Create(filename)
	if err != nil {
		return err
	}
	defer f.Close()
	if err := p.writeRoot(f, root); err != nil {
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

func (p *Pack) overwriteDevice(dev string, root *FileInfo, rootDeviceFiles []deviceconfig.RootFile) error {
	log := p.Env.Logger()

	if err := verifyNotMounted(dev); err != nil {
		return err
	}
	parttable := "GPT + Hybrid MBR"
	if !p.UseGPT {
		parttable = "no GPT, only MBR"
	}
	log.Printf("partitioning %s (%s)", dev, parttable)

	f, err := p.partition(p.Cfg.InternalCompatibilityFlags.Overwrite)
	if err != nil {
		return err
	}
	defer f.Close()

	if _, err := f.Seek(p.FirstPartitionOffsetSectors*512, io.SeekStart); err != nil {
		return err
	}

	if err := p.writeBoot(f, ""); err != nil {
		return err
	}

	if err := p.writeMBR(p.FirstPartitionOffsetSectors, &offsetReadSeeker{f, p.FirstPartitionOffsetSectors * 512}, f, p.Partuuid); err != nil {
		return err
	}

	if _, err := f.Seek((p.FirstPartitionOffsetSectors+(100*MB/512))*512, io.SeekStart); err != nil {
		return err
	}

	tmp, err := os.CreateTemp("", "gokr-packer")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	defer tmp.Close()

	if err := p.writeRoot(tmp, root); err != nil {
		return err
	}
	if _, err := tmp.Seek(0, io.SeekStart); err != nil {
		return err
	}

	if _, err := io.Copy(f, tmp); err != nil {
		return err
	}

	if err := p.writeRootDeviceFiles(f, rootDeviceFiles); err != nil {
		return err
	}

	if err := f.Close(); err != nil {
		return err
	}

	log.Printf("If your applications need to store persistent data, unplug and re-plug the SD card, then create a file system using e.g.:")
	log.Printf("")
	partition := partitionPath(dev, "4")
	if p.ModifyCmdlineRoot() {
		partition = fmt.Sprintf("/dev/disk/by-partuuid/%s", p.PermUUID())
	} else {
		if target, err := filepath.EvalSymlinks(dev); err == nil {
			partition = partitionPath(target, "4")
		}
	}
	log.Printf("\tmkfs.ext4 %s", partition)
	log.Printf("")

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

func (p *Pack) overwriteFile(root *FileInfo, rootDeviceFiles []deviceconfig.RootFile, firstPartitionOffsetSectors int64) (bootSize int64, rootSize int64, err error) {
	log := p.Env.Logger()

	f, err := os.Create(p.Cfg.InternalCompatibilityFlags.Overwrite)
	if err != nil {
		return 0, 0, err
	}

	if err := f.Truncate(int64(p.Cfg.InternalCompatibilityFlags.TargetStorageBytes)); err != nil {
		return 0, 0, err
	}

	if err := p.Partition(f, uint64(p.Cfg.InternalCompatibilityFlags.TargetStorageBytes)); err != nil {
		return 0, 0, err
	}

	if _, err := f.Seek(p.FirstPartitionOffsetSectors*512, io.SeekStart); err != nil {
		return 0, 0, err
	}
	var bs countingWriter
	if err := p.writeBoot(io.MultiWriter(f, &bs), ""); err != nil {
		return 0, 0, err
	}

	if err := p.writeMBR(p.FirstPartitionOffsetSectors, &offsetReadSeeker{f, p.FirstPartitionOffsetSectors * 512}, f, p.Partuuid); err != nil {
		return 0, 0, err
	}

	if _, err := f.Seek(p.FirstPartitionOffsetSectors*512+100*MB, io.SeekStart); err != nil {
		return 0, 0, err
	}

	tmp, err := os.CreateTemp("", "gokr-packer")
	if err != nil {
		return 0, 0, err
	}
	defer os.Remove(tmp.Name())
	defer tmp.Close()

	if err := p.writeRoot(tmp, root); err != nil {
		return 0, 0, err
	}
	if _, err := tmp.Seek(0, io.SeekStart); err != nil {
		return 0, 0, err
	}

	var rs countingWriter
	if _, err := io.Copy(io.MultiWriter(f, &rs), tmp); err != nil {
		return 0, 0, err
	}

	if err := p.writeRootDeviceFiles(f, rootDeviceFiles); err != nil {
		return 0, 0, err
	}

	log.Printf("If your applications need to store persistent data, create a file system using e.g.:")
	log.Printf("\t/sbin/mkfs.ext4 -F -E offset=%v %s %v", p.FirstPartitionOffsetSectors*512+1100*MB, p.Cfg.InternalCompatibilityFlags.Overwrite, packer.PermSizeInKB(firstPartitionOffsetSectors, uint64(p.Cfg.InternalCompatibilityFlags.TargetStorageBytes)))
	log.Printf("")

	return int64(bs), int64(rs), f.Close()
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
	basenames                   map[string]string
	schema                      string
	update                      *config.UpdateStruct
	root                        *FileInfo
	sbom                        []byte
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

func (pack *Pack) logicPrepare(ctx context.Context, programName string, sbomHook func(marshaled []byte, withHash SBOMWithHash)) error {
	log := pack.Env.Logger()
	cfg := pack.Cfg
	tlsflag.SetInsecure(cfg.InternalCompatibilityFlags.Insecure)
	tlsflag.SetUseTLS(cfg.Update.UseTLS)

	if cfg.InternalCompatibilityFlags.Update != "" &&
		cfg.InternalCompatibilityFlags.Overwrite != "" {
		return fmt.Errorf("both -update and -overwrite are specified; use either one, not both")
	}

	// Check early on if the destination is a device that is mounted
	// so that the user does not get the impression that everything
	// is fine and about to complete after a lengthy build phase.
	// See also https://github.com/gokrazy/gokrazy/discussions/308
	switch {
	case cfg.InternalCompatibilityFlags.Overwrite != "" ||
		(pack.Output != nil && pack.Output.Type == OutputTypeFull && pack.Output.Path != ""):

		target := cfg.InternalCompatibilityFlags.Overwrite
		st, err := os.Stat(target)
		if err != nil && !os.IsNotExist(err) {
			return err
		}

		if err == nil && st.Mode()&os.ModeDevice == os.ModeDevice {
			if err := verifyNotMounted(target); err != nil {
				return fmt.Errorf("cannot overwrite %s: %v (perhaps automatically?)\n  please unmount all partitions and retry", target, err)
			}
		}
	}

	var mbrOnlyWithoutGpt bool
	pack.firstPartitionOffsetSectors = deviceconfig.DefaultBootPartitionStartLBA
	if cfg.DeviceType != "" {
		if devcfg, ok := deviceconfig.GetDeviceConfigBySlug(cfg.DeviceType); ok {
			pack.rootDeviceFiles = devcfg.RootDeviceFiles
			mbrOnlyWithoutGpt = devcfg.MBROnlyWithoutGPT
			if devcfg.BootPartitionStartLBA != 0 {
				pack.firstPartitionOffsetSectors = devcfg.BootPartitionStartLBA
			}
		} else {
			return fmt.Errorf("unknown device slug %q", cfg.DeviceType)
		}
	}

	pack.Pack = packer.NewPackForHost(pack.firstPartitionOffsetSectors, cfg.Hostname)

	newInstallation := cfg.InternalCompatibilityFlags.Update == ""
	useGPT := newInstallation && !mbrOnlyWithoutGpt

	pack.Pack.UsePartuuid = newInstallation
	pack.Pack.UseGPTPartuuid = useGPT
	pack.Pack.UseGPT = useGPT

	if os.Getenv("GOKR_PACKER_FD") != "" { // partitioning child process
		if _, err := pack.SudoPartition(cfg.InternalCompatibilityFlags.Overwrite); err != nil {
			log.Printf("%s", err)
			os.Exit(1)
		}
		os.Exit(0)
	}

	log.Printf("%s %s on GOARCH=%s GOOS=%s",
		programName,
		version.ReadBrief(),
		runtime.GOARCH,
		runtime.GOOS)
	log.Printf("")

	if cfg.InternalCompatibilityFlags.Update != "" {
		// TODO: fix update URL:
		log.Printf("Updating gokrazy installation on http://%s", cfg.Hostname)
		log.Printf("")
	}

	log.Printf("Build target: %s", strings.Join(filterGoEnv(packer.Env()), " "))

	pack.buildTimestamp = time.Now().Format(time.RFC3339)
	if ts, ok := ctx.Value(BuildTimestampOverride).(string); ok {
		pack.buildTimestamp = ts
	}
	log.Printf("Build timestamp: %s", pack.buildTimestamp)

	systemCertsPEM, err := pack.findSystemCertsPEM()
	if err != nil {
		return err
	}
	pack.systemCertsPEM = systemCertsPEM

	packageBuildFlags, err := pack.findBuildFlagsFiles(cfg)
	if err != nil {
		return err
	}
	pack.packageBuildFlags = packageBuildFlags

	packageBuildTags, err := pack.findBuildTagsFiles(cfg)
	if err != nil {
		return err
	}
	pack.packageBuildTags = packageBuildTags

	packageBuildEnv, err := findBuildEnv(cfg)
	if err != nil {
		return err
	}
	pack.packageBuildEnv = packageBuildEnv

	flagFileContents, err := pack.findFlagFiles(cfg)
	if err != nil {
		return err
	}
	pack.flagFileContents = flagFileContents

	envFileContents, err := pack.findEnvFiles(cfg)
	if err != nil {
		return err
	}
	pack.envFileContents = envFileContents

	dontStart, err := pack.findDontStart(cfg)
	if err != nil {
		return err
	}
	pack.dontStart = dontStart

	waitForClock, err := pack.findWaitForClock(cfg)
	if err != nil {
		return err
	}
	pack.waitForClock = waitForClock

	basenames, err := findBasenames(cfg)
	if err != nil {
		return err
	}
	pack.basenames = basenames

	return nil
}

func (pack *Pack) logicBuild(programName string, sbomHook func(marshaled []byte, withHash SBOMWithHash), bindir string) error {
	log := pack.Env.Logger()

	cfg := pack.Cfg // for convenience
	args := cfg.Packages
	log.Printf("Building %d Go packages:", len(args))
	log.Printf("")
	for _, pkg := range args {
		log.Printf("  %s", pkg)
		for _, configFile := range packageConfigFiles[pkg] {
			log.Printf("    will %s",
				configFile.kind)
			if configFile.path != "" {
				log.Printf("      from %s",
					configFile.path)
			}
			if !configFile.lastModified.IsZero() {
				log.Printf("      last modified: %s (%s ago)",
					configFile.lastModified.Format(time.RFC3339),
					time.Since(configFile.lastModified).Round(1*time.Second))
			}
		}
		log.Printf("")
	}

	pkgs := append([]string{}, cfg.GokrazyPackagesOrDefault()...)
	pkgs = append(pkgs, cfg.Packages...)
	pkgs = append(pkgs, packer.InitDeps(cfg.InternalCompatibilityFlags.InitPkg)...)
	noBuildPkgs := []string{
		cfg.KernelPackageOrDefault(),
	}
	if fw := cfg.FirmwarePackageOrDefault(); fw != "" {
		noBuildPkgs = append(noBuildPkgs, fw)
	}
	if e := cfg.EEPROMPackageOrDefault(); e != "" {
		noBuildPkgs = append(noBuildPkgs, e)
	}
	setUmask()
	buildEnv := &packer.BuildEnv{
		BuildDir: packer.BuildDirOrMigrate,
	}
	var buildErr error
	trace.WithRegion(context.Background(), "build", func() {
		buildErr = buildEnv.Build(bindir, pkgs, pack.packageBuildFlags, pack.packageBuildTags, pack.packageBuildEnv, noBuildPkgs, pack.basenames)
	})
	if buildErr != nil {
		return buildErr
	}

	log.Printf("")

	var err error
	trace.WithRegion(context.Background(), "validate", func() {
		err = pack.validateTargetArchMatchesKernel()
	})
	if err != nil {
		return err
	}

	var (
		root      *FileInfo
		foundBins []foundBin
	)
	trace.WithRegion(context.Background(), "findbins", func() {
		root, foundBins, err = findBins(cfg, buildEnv, bindir, pack.basenames)
	})
	if err != nil {
		return err
	}
	pack.root = root

	packageConfigFiles = make(map[string][]packageConfigFile)

	var extraFiles map[string][]*FileInfo
	trace.WithRegion(context.Background(), "findextrafiles", func() {
		extraFiles, err = FindExtraFiles(cfg)
	})
	if err != nil {
		return err
	}
	for _, packageExtraFiles := range extraFiles {
		for _, ef := range packageExtraFiles {
			for _, de := range ef.Dirents {
				if de.Filename != "perm" {
					continue
				}
				return fmt.Errorf("invalid ExtraFilePaths or ExtraFileContents: cannot write extra files to user-controlled /perm partition")
			}
		}
	}

	if len(packageConfigFiles) > 0 {
		log.Printf("Including extra files for Go packages:")
		log.Printf("")
		for _, pkg := range args {
			if len(packageConfigFiles[pkg]) == 0 {
				continue
			}
			log.Printf("  %s", pkg)
			for _, configFile := range packageConfigFiles[pkg] {
				log.Printf("    will %s",
					configFile.kind)
				log.Printf("      from %s",
					configFile.path)
				log.Printf("      last modified: %s (%s ago)",
					configFile.lastModified.Format(time.RFC3339),
					time.Since(configFile.lastModified).Round(1*time.Second))
			}
			log.Printf("")
		}
	}

	if cfg.InternalCompatibilityFlags.InitPkg == "" {
		gokrazyInit := &gokrazyInit{
			root:             root,
			flagFileContents: pack.flagFileContents,
			envFileContents:  pack.envFileContents,
			buildTimestamp:   pack.buildTimestamp,
			dontStart:        pack.dontStart,
			waitForClock:     pack.waitForClock,
			basenames:        pack.basenames,
		}
		if cfg.InternalCompatibilityFlags.OverwriteInit != "" {
			return gokrazyInit.dump(cfg.InternalCompatibilityFlags.OverwriteInit)
		}

		var tmpdir string
		trace.WithRegion(context.Background(), "buildinit", func() {
			tmpdir, err = gokrazyInit.build()
		})
		if err != nil {
			return err
		}
		pack.initTmp = tmpdir

		initPath := filepath.Join(tmpdir, "init")

		fileIsELFOrFatal(initPath)

		gokrazy := root.mustFindDirent("gokrazy")
		gokrazy.Dirents = append(gokrazy.Dirents, &FileInfo{
			Filename: "init",
			FromHost: initPath,
		})
	}

	defaultPassword, updateHostname := updateflag.Value{
		Update: cfg.InternalCompatibilityFlags.Update,
	}.GetUpdateTarget(cfg.Hostname)
	update, err := cfg.Update.WithFallbackToHostSpecific(cfg.Hostname)
	if err != nil {
		return err
	}

	if update.HTTPPort == "" {
		update.HTTPPort = "80"
	}

	if update.HTTPSPort == "" {
		update.HTTPSPort = "443"
	}

	if update.Hostname == "" {
		update.Hostname = updateHostname
	}

	if update.HTTPPassword == "" && !update.NoPassword {
		pw, err := ensurePasswordFileExists(updateHostname, defaultPassword)
		if err != nil {
			return err
		}
		update.HTTPPassword = pw
	}

	pack.update = update

	for _, dir := range []string{"bin", "dev", "etc", "proc", "sys", "tmp", "perm", "lib", "run", "mnt"} {
		root.Dirents = append(root.Dirents, &FileInfo{
			Filename: dir,
		})
	}

	root.Dirents = append(root.Dirents, &FileInfo{
		Filename:    "var",
		SymlinkDest: "/perm/var",
	})

	mnt := root.mustFindDirent("mnt")
	for _, md := range cfg.MountDevices {
		if !strings.HasPrefix(md.Target, "/mnt/") {
			continue
		}
		rest := strings.TrimPrefix(md.Target, "/mnt/")
		rest = strings.TrimSuffix(rest, "/")
		if strings.Contains(rest, "/") {
			continue
		}
		mnt.Dirents = append(mnt.Dirents, &FileInfo{
			Filename: rest,
		})
	}

	// include lib/modules from kernelPackage dir, if present
	kernelDir, err := packer.PackageDir(cfg.KernelPackageOrDefault())
	if err != nil {
		return err
	}
	pack.kernelDir = kernelDir
	modulesDir := filepath.Join(kernelDir, "lib", "modules")
	if _, err := os.Stat(modulesDir); err == nil {
		log.Printf("Including loadable kernel modules from:")
		log.Printf("  %s", modulesDir)
		modules := &FileInfo{
			Filename: "modules",
		}
		trace.WithRegion(context.Background(), "kernelmod", func() {
			_, err = addToFileInfo(modules, modulesDir)
		})
		if err != nil {
			return err
		}
		lib := root.mustFindDirent("lib")
		lib.Dirents = append(lib.Dirents, modules)
	}

	etc := root.mustFindDirent("etc")
	tmpdir, err := os.MkdirTemp("", "gokrazy")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmpdir)
	hostLocaltime, err := hostLocaltime(tmpdir)
	if err != nil {
		return err
	}
	if hostLocaltime != "" {
		etc.Dirents = append(etc.Dirents, &FileInfo{
			Filename: "localtime",
			FromHost: hostLocaltime,
		})
	}
	etc.Dirents = append(etc.Dirents, &FileInfo{
		Filename:    "resolv.conf",
		SymlinkDest: "/tmp/resolv.conf",
	})
	etc.Dirents = append(etc.Dirents, &FileInfo{
		Filename: "hosts",
		FromLiteral: `127.0.0.1 localhost
::1 localhost
`,
	})
	etc.Dirents = append(etc.Dirents, &FileInfo{
		Filename:    "hostname",
		FromLiteral: cfg.Hostname,
	})

	ssl := &FileInfo{Filename: "ssl"}
	ssl.Dirents = append(ssl.Dirents, &FileInfo{
		Filename:    "ca-bundle.pem",
		FromLiteral: pack.systemCertsPEM,
	})

	pack.schema = "http"
	if update.CertPEM == "" || update.KeyPEM == "" {
		deployCertFile, deployKeyFile, err := getCertificate(cfg)
		if err != nil {
			return err
		}

		if deployCertFile != "" {
			b, err := os.ReadFile(deployCertFile)
			if err != nil {
				return err
			}
			update.CertPEM = strings.TrimSpace(string(b))

			b, err = os.ReadFile(deployKeyFile)
			if err != nil {
				return err
			}
			update.KeyPEM = strings.TrimSpace(string(b))
		}
	}
	if update.CertPEM != "" && update.KeyPEM != "" {
		// User requested TLS
		if tlsflag.Insecure() {
			// If -insecure is specified, use http instead of https to make the
			// process of updating to non-empty -tls= a bit smoother.
		} else {
			pack.schema = "https"
		}

		ssl.Dirents = append(ssl.Dirents, &FileInfo{
			Filename:    "gokrazy-web.pem",
			FromLiteral: update.CertPEM,
		})
		ssl.Dirents = append(ssl.Dirents, &FileInfo{
			Filename:    "gokrazy-web.key.pem",
			FromLiteral: update.KeyPEM,
		})
	}

	etc.Dirents = append(etc.Dirents, ssl)

	if !update.NoPassword {
		etc.Dirents = append(etc.Dirents, &FileInfo{
			Filename:    "gokr-pw.txt",
			Mode:        0400,
			FromLiteral: update.HTTPPassword,
		})
	}

	etc.Dirents = append(etc.Dirents, &FileInfo{
		Filename:    "http-port.txt",
		FromLiteral: update.HTTPPort,
	})

	etc.Dirents = append(etc.Dirents, &FileInfo{
		Filename:    "https-port.txt",
		FromLiteral: update.HTTPSPort,
	})

	// GenerateSBOM() must be provided with a cfg
	// that hasn't been modified by gok at runtime,
	// as the SBOM should reflect what’s going into gokrazy,
	// not its internal implementation details
	// (i.e.  cfg.InternalCompatibilityFlags untouched).
	var sbom []byte
	var sbomWithHash SBOMWithHash
	trace.WithRegion(context.Background(), "sbom", func() {
		sbom, sbomWithHash, err = generateSBOM(pack.FileCfg, foundBins)
	})
	if err != nil {
		return err
	}
	pack.sbom = sbom

	// TODO: This is a terrible hack. After removing gokr-packer
	// (https://github.com/gokrazy/gokrazy/issues/301), we should refactor this
	// overly long method into more manageable chunks.
	if sbomHook != nil {
		sbomHook(sbom, sbomWithHash)
		return nil
	}

	etcGokrazy := &FileInfo{Filename: "gokrazy"}
	etcGokrazy.Dirents = append(etcGokrazy.Dirents, &FileInfo{
		Filename:    "sbom.json",
		FromLiteral: string(sbom),
	})
	mountdevices, err := json.Marshal(cfg.MountDevices)
	if err != nil {
		return err
	}
	etcGokrazy.Dirents = append(etcGokrazy.Dirents, &FileInfo{
		Filename:    "mountdevices.json",
		FromLiteral: string(mountdevices),
	})
	etc.Dirents = append(etc.Dirents, etcGokrazy)

	empty := &FileInfo{Filename: ""}
	if paths := getDuplication(root, empty); len(paths) > 0 {
		return fmt.Errorf("root file system contains duplicate files: your config contains multiple packages that install %s", paths)
	}

	for pkg1, fs := range extraFiles {
		for _, fs1 := range fs {
			// check against root fs
			if paths := getDuplication(root, fs1); len(paths) > 0 {
				return fmt.Errorf("extra files of package %s collides with root file system: %v", pkg1, paths)
			}

			// check against other packages
			for pkg2, fs := range extraFiles {
				for _, fs2 := range fs {
					if pkg1 == pkg2 {
						continue
					}

					if paths := getDuplication(fs1, fs2); len(paths) > 0 {
						return fmt.Errorf("extra files of package %s collides with package %s: %v", pkg1, pkg2, paths)
					}
				}
			}

			// add extra files to rootfs
			if err := root.combine(fs1); err != nil {
				return fmt.Errorf("failed to add extra files from package %s: %v", pkg1, err)
			}
		}
	}

	return nil
}

func (pack *Pack) logic(ctx context.Context, programName string, sbomHook func(marshaled []byte, withHash SBOMWithHash)) error {
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

	if err := pack.logicPrepare(ctx, programName, sbomHook); err != nil {
		return err
	}

	bindir, err := os.MkdirTemp("", "gokrazy-bins-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(bindir)

	if err := pack.logicBuild(programName, sbomHook, bindir); err != nil {
		return err
	}
	defer os.RemoveAll(pack.initTmp)

	if err := pack.logicWrite(programName, sbomHook, bindir, dnsCheck); err != nil {
		return err
	}

	return nil
}

func (pack *Pack) logicWrite(programName string, sbomHook func(marshaled []byte, withHash SBOMWithHash), bindir string, dnsCheck chan error) error {
	ctx := context.Background()
	log := pack.Env.Logger()

	var (
		updateHttpClient         *http.Client
		foundMatchingCertificate bool
		updateBaseUrl            *url.URL
		target                   *updater.Target
	)

	newInstallation := pack.Cfg.InternalCompatibilityFlags.Update == ""
	if !newInstallation {
		update := pack.update // for convenience
		var err error
		updateBaseUrl, err = updateflag.Value{
			Update: pack.Cfg.InternalCompatibilityFlags.Update,
		}.BaseURL(update.HTTPPort, update.HTTPSPort, pack.schema, update.Hostname, update.HTTPPassword)
		if err != nil {
			return err
		}

		updateHttpClient, foundMatchingCertificate, err = httpclient.GetTLSHttpClientByTLSFlag(tlsflag.GetUseTLS(), tlsflag.GetInsecure(), updateBaseUrl)
		if err != nil {
			return fmt.Errorf("getting http client by tls flag: %v", err)
		}
		done := measure.Interactively("probing https")
		remoteScheme, err := httpclient.GetRemoteScheme(updateBaseUrl)
		done("")
		if remoteScheme == "https" {
			updateBaseUrl, err = updateflag.Value{
				Update: pack.Cfg.InternalCompatibilityFlags.Update,
			}.BaseURL(update.HTTPPort, update.HTTPSPort, "https", update.Hostname, update.HTTPPassword)
			if err != nil {
				return err
			}
			pack.Cfg.InternalCompatibilityFlags.Update = updateBaseUrl.String()
		}

		if updateBaseUrl.Scheme != "https" && foundMatchingCertificate {
			log.Printf("")
			log.Printf("!!!WARNING!!! Possible SSL-Stripping detected!")
			log.Printf("Found certificate for hostname in your client configuration but the host does not offer https!")
			log.Printf("")
			if !tlsflag.Insecure() {
				log.Printf("update canceled: TLS certificate found, but negotiating a TLS connection with the target failed")
				os.Exit(1)
			}
			log.Printf("Proceeding anyway as requested (--insecure).")
		}

		// Opt out of PARTUUID= for updating until we can check the remote
		// userland version is new enough to understand how to set the active
		// root partition when PARTUUID= is in use.
		if err != nil {
			return err
		}
		updateBaseUrl.Path = "/"

		target, err = updater.NewTarget(ctx, updateBaseUrl.String(), updateHttpClient)
		if err != nil {
			return fmt.Errorf("checking target partuuid support: %v", err)
		}
		pack.UsePartuuid = target.Supports("partuuid")
		pack.UseGPTPartuuid = target.Supports("gpt")
		pack.UseGPT = target.Supports("gpt")
		pack.ExistingEEPROM = target.InstalledEEPROM()
	}
	log.Printf("")
	log.Printf("Feature summary:")
	log.Printf("  use GPT: %v", pack.UseGPT)
	log.Printf("  use PARTUUID: %v", pack.UsePartuuid)
	log.Printf("  use GPT PARTUUID: %v", pack.UseGPTPartuuid)

	cfg := pack.Cfg   // for convenience
	root := pack.root // for convenience
	// Determine where to write the boot and root images to.
	var (
		isDev                    bool
		tmpBoot, tmpRoot, tmpMBR *os.File
		bootSize, rootSize       int64
	)
	switch {
	case cfg.InternalCompatibilityFlags.Overwrite != "" ||
		(pack.Output != nil && pack.Output.Type == OutputTypeFull && pack.Output.Path != ""):

		st, err := os.Stat(cfg.InternalCompatibilityFlags.Overwrite)
		if err != nil && !os.IsNotExist(err) {
			return err
		}

		isDev = err == nil && st.Mode()&os.ModeDevice == os.ModeDevice

		if isDev {
			if err := pack.overwriteDevice(cfg.InternalCompatibilityFlags.Overwrite, root, pack.rootDeviceFiles); err != nil {
				return err
			}
			log.Printf("To boot gokrazy, plug the SD card into a supported device (see https://gokrazy.org/platforms/)")
			log.Printf("")
		} else {
			lower := 1200*MB + int(pack.firstPartitionOffsetSectors)

			if cfg.InternalCompatibilityFlags.TargetStorageBytes == 0 {
				return fmt.Errorf("--target_storage_bytes is required (e.g. --target_storage_bytes=%d) when using overwrite with a file", lower)
			}
			if cfg.InternalCompatibilityFlags.TargetStorageBytes%512 != 0 {
				return fmt.Errorf("--target_storage_bytes must be a multiple of 512 (sector size), use e.g. %d", lower)
			}
			if cfg.InternalCompatibilityFlags.TargetStorageBytes < lower {
				return fmt.Errorf("--target_storage_bytes must be at least %d (for boot + 2 root file systems + 100 MB /perm)", lower)
			}

			bootSize, rootSize, err = pack.overwriteFile(root, pack.rootDeviceFiles, pack.firstPartitionOffsetSectors)
			if err != nil {
				return err
			}

			log.Printf("To boot gokrazy, copy %s to an SD card and plug it into a supported device (see https://gokrazy.org/platforms/)", cfg.InternalCompatibilityFlags.Overwrite)
			log.Printf("")
		}

	case pack.Output != nil && pack.Output.Type == OutputTypeGaf && pack.Output.Path != "":
		if err := pack.overwriteGaf(root, pack.sbom); err != nil {
			return err
		}

	default:
		if cfg.InternalCompatibilityFlags.OverwriteBoot != "" {
			mbrfn := cfg.InternalCompatibilityFlags.OverwriteMBR
			if cfg.InternalCompatibilityFlags.OverwriteMBR == "" {
				var err error
				tmpMBR, err = os.CreateTemp("", "gokrazy")
				if err != nil {
					return err
				}
				defer os.Remove(tmpMBR.Name())
				mbrfn = tmpMBR.Name()
			}
			if err := pack.writeBootFile(cfg.InternalCompatibilityFlags.OverwriteBoot, mbrfn); err != nil {
				return err
			}
		}

		if cfg.InternalCompatibilityFlags.OverwriteRoot != "" {
			var rootErr error
			trace.WithRegion(context.Background(), "writeroot", func() {
				rootErr = pack.writeRootFile(cfg.InternalCompatibilityFlags.OverwriteRoot, root)
			})
			if rootErr != nil {
				return rootErr
			}
		}

		if cfg.InternalCompatibilityFlags.OverwriteBoot == "" && cfg.InternalCompatibilityFlags.OverwriteRoot == "" {
			var err error
			tmpMBR, err = os.CreateTemp("", "gokrazy")
			if err != nil {
				return err
			}
			defer os.Remove(tmpMBR.Name())

			tmpBoot, err = os.CreateTemp("", "gokrazy")
			if err != nil {
				return err
			}
			defer os.Remove(tmpBoot.Name())

			if err := pack.writeBoot(tmpBoot, tmpMBR.Name()); err != nil {
				return err
			}

			tmpRoot, err = os.CreateTemp("", "gokrazy")
			if err != nil {
				return err
			}
			defer os.Remove(tmpRoot.Name())

			if err := pack.writeRoot(tmpRoot, root); err != nil {
				return err
			}
		}
	}

	log.Printf("")
	log.Printf("Build complete!")

	update := pack.update // for convenience
	hostPort := update.Hostname
	if hostPort == "" {
		hostPort = cfg.Hostname
	}
	if pack.schema == "http" && update.HTTPPort != "80" {
		hostPort = fmt.Sprintf("%s:%s", hostPort, update.HTTPPort)
	}
	if pack.schema == "https" && update.HTTPSPort != "443" {
		hostPort = fmt.Sprintf("%s:%s", hostPort, update.HTTPSPort)
	}

	log.Printf("")
	log.Printf("To interact with the device, gokrazy provides a web interface reachable at:")
	log.Printf("")
	log.Printf("\t%s://gokrazy:%s@%s/", pack.schema, update.HTTPPassword, hostPort)
	log.Printf("")
	log.Printf("In addition, the following Linux consoles are set up:")
	log.Printf("")
	if cfg.SerialConsoleOrDefault() != "disabled" {
		log.Printf("\t1. foreground Linux console on the serial port (115200n8, pin 6, 8, 10 for GND, TX, RX), accepting input")
		log.Printf("\t2. secondary Linux framebuffer console on HDMI; shows Linux kernel message but no init system messages")
	} else {
		log.Printf("\t1. foreground Linux framebuffer console on HDMI")
	}

	if cfg.SerialConsoleOrDefault() != "disabled" {
		log.Printf("")
		log.Printf("Use -serial_console=disabled to make gokrazy not touch the serial port, and instead make the framebuffer console on HDMI the foreground console")
	}
	log.Printf("")
	if pack.schema == "https" {
		certObj, err := getCertificateFromString(update.CertPEM)
		if err != nil {
			return fmt.Errorf("error loading certificate: %v", err)
		} else {
			log.Printf("")
			log.Printf("The TLS Certificate of the gokrazy web interface is located under")
			log.Printf("\t%s", cfg.Meta.Path)
			log.Printf("The fingerprint of the Certificate is")
			log.Printf("\t%x", getCertificateFingerprintSHA1(certObj))
			log.Printf("The certificate is valid until")
			log.Printf("\t%s", certObj.NotAfter.String())
			log.Printf("Please verify the certificate, before adding an exception to your browser!")
		}
	}

	if err := <-dnsCheck; err != nil {
		log.Printf("WARNING: if the above URL does not work, perhaps name resolution (DNS) is broken")
		log.Printf("in your local network? Resolving your hostname failed: %v", err)
		log.Printf("Did you maybe configure a DNS server other than your router?")
		log.Printf("")
	}

	if newInstallation {
		return nil
	}

	// Determine where to read the boot, root and MBR images from.
	var rootReader, bootReader, mbrReader io.Reader
	switch {
	case cfg.InternalCompatibilityFlags.Overwrite != "":
		if isDev {
			bootFile, err := os.Open(cfg.InternalCompatibilityFlags.Overwrite + "1")
			if err != nil {
				return err
			}
			bootReader = bootFile
			rootFile, err := os.Open(cfg.InternalCompatibilityFlags.Overwrite + "2")
			if err != nil {
				return err
			}
			rootReader = rootFile
		} else {
			bootFile, err := os.Open(cfg.InternalCompatibilityFlags.Overwrite)
			if err != nil {
				return err
			}
			if _, err := bootFile.Seek(pack.firstPartitionOffsetSectors*512, io.SeekStart); err != nil {
				return err
			}
			bootReader = &io.LimitedReader{
				R: bootFile,
				N: bootSize,
			}

			rootFile, err := os.Open(cfg.InternalCompatibilityFlags.Overwrite)
			if err != nil {
				return err
			}
			if _, err := rootFile.Seek(pack.firstPartitionOffsetSectors*512+100*MB, io.SeekStart); err != nil {
				return err
			}
			rootReader = &io.LimitedReader{
				R: rootFile,
				N: rootSize,
			}
		}
		mbrFile, err := os.Open(cfg.InternalCompatibilityFlags.Overwrite)
		if err != nil {
			return err
		}
		mbrReader = &io.LimitedReader{
			R: mbrFile,
			N: 446,
		}

	default:
		if cfg.InternalCompatibilityFlags.OverwriteBoot != "" {
			bootFile, err := os.Open(cfg.InternalCompatibilityFlags.OverwriteBoot)
			if err != nil {
				return err
			}
			bootReader = bootFile
			if cfg.InternalCompatibilityFlags.OverwriteMBR != "" {
				mbrFile, err := os.Open(cfg.InternalCompatibilityFlags.OverwriteMBR)
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

		if cfg.InternalCompatibilityFlags.OverwriteRoot != "" {
			rootFile, err := os.Open(cfg.InternalCompatibilityFlags.OverwriteRoot)
			if err != nil {
				return err
			}
			rootReader = rootFile
		}

		if cfg.InternalCompatibilityFlags.OverwriteBoot == "" && cfg.InternalCompatibilityFlags.OverwriteRoot == "" {
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
	log.Printf("Updating %s", updateBaseUrl.String())

	progctx, canc := context.WithCancel(context.Background())
	defer canc()
	prog := &progress.Reporter{}
	go prog.Report(progctx)

	// Start with the root file system because writing to the non-active
	// partition cannot break the currently running system.
	if err := pack.updateWithProgress(prog, rootReader, target, "root file system", "root"); err != nil {
		return err
	}

	for _, rootDeviceFile := range pack.rootDeviceFiles {
		f, err := os.Open(filepath.Join(pack.kernelDir, rootDeviceFile.Name))
		if err != nil {
			return err
		}

		if err := pack.updateWithProgress(
			prog, f, target, fmt.Sprintf("root device file %s", rootDeviceFile.Name),
			filepath.Join("device-specific", rootDeviceFile.Name),
		); err != nil {
			if errors.Is(err, updater.ErrUpdateHandlerNotImplemented) {
				log.Printf("target does not support updating device file %s yet, ignoring", rootDeviceFile.Name)
				continue
			}
			return err
		}
	}

	if err := pack.updateWithProgress(prog, bootReader, target, "boot file system", "boot"); err != nil {
		return err
	}

	if err := target.StreamTo(ctx, "mbr", mbrReader); err != nil {
		if err == updater.ErrUpdateHandlerNotImplemented {
			log.Printf("target does not support updating MBR yet, ignoring")
		} else {
			return fmt.Errorf("updating MBR: %v", err)
		}
	}

	if cfg.InternalCompatibilityFlags.Testboot {
		if err := target.Testboot(ctx); err != nil {
			return fmt.Errorf("enable testboot of non-active partition: %v", err)
		}
	} else {
		if err := target.Switch(ctx); err != nil {
			return fmt.Errorf("switching to non-active partition: %v", err)
		}
	}

	// Stop progress reporting to not mess up the following logs output.
	canc()

	log.Printf("Triggering reboot")
	if err := target.Reboot(ctx); err != nil {
		if errors.Is(err, syscall.ECONNRESET) {
			log.Printf("ignoring reboot error: %v", err)
		} else {
			return fmt.Errorf("reboot: %v", err)
		}
	}

	const polltimeout = 5 * time.Minute
	log.Printf("Updated, waiting %v for the device to become reachable (cancel with Ctrl-C any time)", polltimeout)

	if update.CertPEM != "" && update.KeyPEM != "" {
		// Use an HTTPS client (post-update),
		// even when the --insecure flag was specified.
		pack.schema = "https"
		var err error
		updateBaseUrl, err = updateflag.Value{
			Update: "yes",
		}.BaseURL(update.HTTPPort, update.HTTPSPort, pack.schema, update.Hostname, update.HTTPPassword)
		if err != nil {
			return err
		}
		updateHttpClient, foundMatchingCertificate, err = httpclient.GetTLSHttpClientByTLSFlag(tlsflag.GetUseTLS(), false /* insecure */, updateBaseUrl)
		if err != nil {
			return fmt.Errorf("getting http client by tls flag: %v", err)
		}
	}

	pollctx, canc := context.WithTimeout(context.Background(), polltimeout)
	defer canc()
	for {
		if err := pollctx.Err(); err != nil {
			return fmt.Errorf("device did not become healthy after update (%v)", err)
		}
		if err := pollUpdated1(pollctx, updateHttpClient, updateBaseUrl.String(), pack.buildTimestamp); err != nil {
			log.Printf("device not yet reachable: %v", err)
			time.Sleep(1 * time.Second)
			continue
		}

		log.Printf("Device ready to use!")
		break
	}

	return nil
}

// kernelGoarch returns the GOARCH value that corresponds to the provided
// vmlinuz header. It returns one of "arm", "arm64", "386", "amd64" or the empty
// string if not detected.
func kernelGoarch(hdr []byte) string {
	// Some constants from the file(1) command's magic.
	const (
		// 32-bit arm: https://github.com/file/file/blob/65be1904/magic/Magdir/linux#L238-L241
		arm32Magic       = 0x016f2818
		arm32MagicOffset = 0x24
		// 64-bit arm: https://github.com/file/file/blob/65be1904/magic/Magdir/linux#L253-L254
		arm64Magic       = 0x644d5241
		arm64MagicOffset = 0x38
		// x86: https://github.com/file/file/blob/65be1904/magic/Magdir/linux#L137-L152
		x86Magic            = 0xaa55
		x86MagicOffset      = 0x1fe
		x86XloadflagsOffset = 0x236
	)
	if len(hdr) >= arm64MagicOffset+4 && binary.LittleEndian.Uint32(hdr[arm64MagicOffset:]) == arm64Magic {
		return "arm64"
	}
	if len(hdr) >= arm32MagicOffset+4 && binary.LittleEndian.Uint32(hdr[arm32MagicOffset:]) == arm32Magic {
		return "arm"
	}
	if len(hdr) >= x86XloadflagsOffset+2 && binary.LittleEndian.Uint16(hdr[x86MagicOffset:]) == x86Magic {
		// XLF0 in arch/x86/boot/header.S
		if hdr[x86XloadflagsOffset]&1 != 0 {
			return "amd64"
		} else {
			return "386"
		}
	}
	return ""
}

// validateTargetArchMatchesKernel validates that the packer.TargetArch
// corresponds to the kernel's architecture.
//
// See https://github.com/gokrazy/gokrazy/issues/191 for background. Maybe the
// TargetArch will become automatic in the future but for now this is a safety
// net to prevent people from bricking their appliances with the wrong userspace
// architecture.
func (pack *Pack) validateTargetArchMatchesKernel() error {
	cfg := pack.Cfg
	kernelDir, err := packer.PackageDir(cfg.KernelPackageOrDefault())
	if err != nil {
		return err
	}
	kernelPath := filepath.Join(kernelDir, "vmlinuz")
	k, err := os.Open(kernelPath)
	if err != nil {
		return err
	}
	defer k.Close()
	hdr := make([]byte, 1<<10) // plenty
	if _, err := io.ReadFull(k, hdr); err != nil {
		return err
	}
	kernelArch := kernelGoarch(hdr)
	if kernelArch == "" {
		return fmt.Errorf("kernel %v architecture in %s not detected", cfg.KernelPackageOrDefault(), kernelPath)
	}
	targetArch := packer.TargetArch()
	if kernelArch != targetArch {
		return fmt.Errorf("target architecture %q (GOARCH) doesn't match the %s kernel type %q",
			targetArch,
			cfg.KernelPackageOrDefault(),
			kernelArch)
	}
	return nil
}

func (pack *Pack) updateWithProgress(prog *progress.Reporter, reader io.Reader, target *updater.Target, logStr string, stream string) error {
	ctx := context.Background()
	log := pack.Env.Logger()

	start := time.Now()
	prog.SetStatus(fmt.Sprintf("update %s", logStr))
	prog.SetTotal(0)

	if stater, ok := reader.(interface{ Stat() (os.FileInfo, error) }); ok {
		if st, err := stater.Stat(); err == nil {
			prog.SetTotal(uint64(st.Size()))
		}
	}
	if err := target.StreamTo(ctx, stream, io.TeeReader(reader, &progress.Writer{})); err != nil {
		return fmt.Errorf("updating %s: %w", logStr, err)
	}
	duration := time.Since(start)
	transferred := progress.Reset()
	log.Printf("\rTransferred %s (%s) at %.2f MiB/s (total: %v)",
		logStr,
		humanize.Bytes(transferred),
		float64(transferred)/duration.Seconds()/1024/1024,
		duration.Round(time.Second))

	return nil
}

func (pack *Pack) Main(ctx context.Context, programName string) {
	if err := pack.logic(ctx, programName, nil); err != nil {
		fmt.Fprintf(os.Stderr, "ERROR:\n  %s\n", err)
		os.Exit(1)
	}
}

func (pack *Pack) GenerateSBOM(ctx context.Context) ([]byte, SBOMWithHash, error) {
	var sbom []byte
	var sbomWithHash SBOMWithHash
	if err := pack.logic(ctx, "gokrazy gok", func(b []byte, wh SBOMWithHash) {
		sbom = b
		sbomWithHash = wh
	}); err != nil {
		return nil, SBOMWithHash{}, err
	}
	return sbom, sbomWithHash, nil
}
