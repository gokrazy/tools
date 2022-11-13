// Package packer builds and deploys a gokrazy image. Called from the old
// gokr-packer binary and the new gok binary.
package packer

import (
	"archive/tar"
	"bufio"
	"context"
	"errors"
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
	"sort"
	"strings"
	"time"

	"github.com/gokrazy/internal/config"
	"github.com/gokrazy/internal/deviceconfig"
	"github.com/gokrazy/internal/httpclient"
	"github.com/gokrazy/internal/humanize"
	"github.com/gokrazy/internal/progress"
	"github.com/gokrazy/internal/tlsflag"
	"github.com/gokrazy/internal/updateflag"
	"github.com/gokrazy/tools/internal/measure"
	"github.com/gokrazy/tools/internal/version"
	"github.com/gokrazy/tools/packer"
	"github.com/gokrazy/updater"
)

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
	for _, pkg := range cfg.Packages {
		buildPackages = append(buildPackages, pkg)
	}
	for _, pkg := range cfg.GokrazyPackagesOrDefault() {
		if strings.TrimSpace(pkg) == "" {
			continue
		}
		buildPackages = append(buildPackages, pkg)
	}
	return buildPackages
}

func findFlagFiles(cfg *config.Struct) (map[string]string, error) {
	if len(cfg.PackageConfig) > 0 {
		contents := make(map[string]string)
		for pkg, packageConfig := range cfg.PackageConfig {
			if len(packageConfig.CommandLineFlags) == 0 {
				continue
			}
			contents[pkg] = strings.Join(packageConfig.CommandLineFlags, "\n")
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

	contents := make(map[string]string)
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

func findBuildFlagsFiles(cfg *config.Struct) (map[string][]string, error) {
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

func findBuildTagsFiles(cfg *config.Struct) (map[string][]string, error) {
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

		b, err := ioutil.ReadFile(p.path)
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

func findEnvFiles(cfg *config.Struct) (map[string]string, error) {
	if len(cfg.PackageConfig) > 0 {
		contents := make(map[string]string)
		for pkg, packageConfig := range cfg.PackageConfig {
			if len(packageConfig.Environment) == 0 {
				continue
			}
			contents[pkg] = strings.Join(packageConfig.Environment, "\n")
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

	contents := make(map[string]string)
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

		parent := ae.dirs[filepath.Dir(filename)]
		parent.Dirents = append(parent.Dirents, fi)

		switch header.Typeflag {
		case tar.TypeSymlink:
			fi.SymlinkDest = header.Linkname

		case tar.TypeDir:
			ae.dirs[filename] = fi

		default:
			// TODO(optimization): do not hold file data in memory, instead
			// stream the archive contents lazily to conserve RAM
			b, err := ioutil.ReadAll(rd)
			if err != nil {
				return time.Time{}, err
			}
			fi.FromLiteral = string(b)
		}
	}

	return latestTime, nil
}

func findExtraFilesInDir(pkg, dir string, fi *FileInfo) error {
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

func findExtraFiles(cfg *config.Struct) (map[string][]*FileInfo, error) {
	extraFiles := make(map[string][]*FileInfo)
	if len(cfg.PackageConfig) > 0 {
		for pkg, packageConfig := range cfg.PackageConfig {
			var fileInfos []*FileInfo

			for dest, path := range packageConfig.ExtraFilePaths {
				root := &FileInfo{}
				if st, err := os.Stat(path); err == nil && st.Mode().IsRegular() {
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
					// Copy a tarball or directory from the host
					dir := mkdirp(root, dest)
					if err := findExtraFilesInDir(pkg, path, dir); err != nil {
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
			if err := findExtraFilesInDir(pkg, dir, root); err != nil {
				return nil, err
			}
			extraFiles[pkg] = append(extraFiles[pkg], root)
		}
		{
			// Look for extra files in <pkg>/_gokrazy/extrafiles/
			dir := packageDirs[idx]
			subdir := filepath.Join(dir, "_gokrazy", "extrafiles")
			root := &FileInfo{}
			if err := findExtraFilesInDir(pkg, subdir, root); err != nil {
				return nil, err
			}
			extraFiles[pkg] = append(extraFiles[pkg], root)
		}
	}

	return extraFiles, nil
}

func findDontStart(cfg *config.Struct) (map[string]bool, error) {
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

		// NOTE: ideally we would use the full package here, but our init
		// template only deals with base names right now.
		contents[filepath.Base(pkg)] = true
	}

	return contents, nil
}

func findWaitForClock(cfg *config.Struct) (map[string]bool, error) {
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

func writeRootFile(filename string, root *FileInfo) error {
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

func (p *Pack) overwriteDevice(dev string, root *FileInfo, rootDeviceFiles []deviceconfig.RootFile) error {
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

	if err := p.writeRootDeviceFiles(f, rootDeviceFiles); err != nil {
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

func (p *Pack) overwriteFile(filename string, root *FileInfo, rootDeviceFiles []deviceconfig.RootFile) (bootSize int64, rootSize int64, err error) {
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

	if err := p.writeRootDeviceFiles(f, rootDeviceFiles); err != nil {
		return 0, 0, err
	}

	fmt.Printf("If your applications need to store persistent data, create a file system using e.g.:\n")
	fmt.Printf("\t/sbin/mkfs.ext4 -F -E offset=%v %s %v\n", 8192*512+1100*MB, p.Cfg.InternalCompatibilityFlags.Overwrite, packer.PermSizeInKB(uint64(p.Cfg.InternalCompatibilityFlags.TargetStorageBytes)))
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

type Pack struct {
	packer.Pack

	Cfg *config.Struct
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

func logic(cfg *config.Struct) error {
	updateflag.SetUpdate(cfg.InternalCompatibilityFlags.Update)
	tlsflag.SetInsecure(cfg.InternalCompatibilityFlags.Insecure)
	tlsflag.SetUseTLS(cfg.Update.UseTLS)

	if !updateflag.NewInstallation() && cfg.InternalCompatibilityFlags.Overwrite != "" {
		return fmt.Errorf("both -update and -overwrite are specified; use either one, not both")
	}

	if cfg.Update.HttpPort == "" {
		cfg.Update.HttpPort = "80"
	}

	if cfg.Update.HttpsPort == "" {
		cfg.Update.HttpsPort = "443"
	}

	if cfg.InternalCompatibilityFlags.Sudo == "" {
		cfg.InternalCompatibilityFlags.Sudo = "auto"
	}

	for _, env := range cfg.InternalCompatibilityFlags.Env {
		parts := strings.Split(env, "=")
		if err := os.Setenv(parts[0], parts[1]); err != nil {
			return err
		}
	}

	fmt.Printf("gokrazy packer %s on GOARCH=%s GOOS=%s\n\n",
		version.ReadBrief(),
		runtime.GOARCH,
		runtime.GOOS)

	if cfg.InternalCompatibilityFlags.Update != "" {
		// TODO: fix update URL:
		fmt.Printf("Updating gokrazy installation on http://%s\n\n", cfg.Hostname)
	}

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

	bindir, err := ioutil.TempDir("", "gokrazy-bins-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(bindir)

	packageBuildFlags, err := findBuildFlagsFiles(cfg)
	if err != nil {
		return err
	}

	packageBuildTags, err := findBuildTagsFiles(cfg)
	if err != nil {
		return err
	}

	flagFileContents, err := findFlagFiles(cfg)
	if err != nil {
		return err
	}

	envFileContents, err := findEnvFiles(cfg)
	if err != nil {
		return err
	}

	dontStart, err := findDontStart(cfg)
	if err != nil {
		return err
	}

	waitForClock, err := findWaitForClock(cfg)
	if err != nil {
		return err
	}

	var mbrOnlyWithoutGpt bool
	var rootDeviceFiles []deviceconfig.RootFile
	if cfg.DeviceType != "" {
		if devcfg, ok := deviceconfig.GetDeviceConfigBySlug(cfg.DeviceType); ok {
			rootDeviceFiles = devcfg.RootDeviceFiles
			mbrOnlyWithoutGpt = devcfg.MBROnlyWithoutGPT
		} else {
			return fmt.Errorf("unknown device slug %q", cfg.DeviceType)
		}
	}

	args := cfg.Packages
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
	buildEnv := &packer.BuildEnv{
		BuildDir: packer.BuildDir,
	}
	if err := buildEnv.Build(bindir, pkgs, packageBuildFlags, packageBuildTags, noBuildPkgs); err != nil {
		return err
	}

	fmt.Println()

	root, err := findBins(cfg, buildEnv, bindir)
	if err != nil {
		return err
	}

	packageConfigFiles = make(map[string][]packageConfigFile)

	extraFiles, err := findExtraFiles(cfg)
	if err != nil {
		return err
	}

	if len(packageConfigFiles) > 0 {
		fmt.Printf("Including extra files for Go packages:\n\n")
		for _, pkg := range args {
			if len(packageConfigFiles[pkg]) == 0 {
				continue
			}
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
	}

	if cfg.InternalCompatibilityFlags.InitPkg == "" {
		gokrazyInit := &gokrazyInit{
			root:             root,
			flagFileContents: flagFileContents,
			envFileContents:  envFileContents,
			buildTimestamp:   buildTimestamp,
			dontStart:        dontStart,
			waitForClock:     waitForClock,
		}
		if cfg.InternalCompatibilityFlags.OverwriteInit != "" {
			return gokrazyInit.dump(cfg.InternalCompatibilityFlags.OverwriteInit)
		}

		tmpdir, err := gokrazyInit.build()
		if err != nil {
			return err
		}
		defer os.RemoveAll(tmpdir)

		initPath := filepath.Join(tmpdir, "init")

		fileIsELFOrFatal(initPath)

		gokrazy := root.mustFindDirent("gokrazy")
		gokrazy.Dirents = append(gokrazy.Dirents, &FileInfo{
			Filename: "init",
			FromHost: initPath,
		})
	}

	defaultPassword, updateHostname := updateflag.GetUpdateTarget(cfg.Hostname)
	update, err := cfg.Update.WithFallbackToHostSpecific(updateHostname)
	if err != nil {
		return err
	}
	if update.HttpPassword == "" {
		pw, err := ensurePasswordFileExists(updateHostname, defaultPassword)
		if err != nil {
			return err
		}
		update.HttpPassword = pw
	}

	for _, dir := range []string{"dev", "etc", "proc", "sys", "tmp", "perm", "lib", "run", "var"} {
		root.Dirents = append(root.Dirents, &FileInfo{
			Filename: dir,
		})
	}

	// include lib/modules from kernelPackage dir, if present
	kernelDir, err := packer.PackageDir(cfg.KernelPackageOrDefault())
	if err != nil {
		return err
	}
	modulesDir := filepath.Join(kernelDir, "lib", "modules")
	if _, err := os.Stat(modulesDir); err == nil {
		fmt.Printf("Including loadable kernel modules from:\n%s\n", modulesDir)
		modules := &FileInfo{
			Filename: "modules",
		}
		_, err := addToFileInfo(modules, modulesDir)
		if err != nil {
			return err
		}
		lib := root.mustFindDirent("lib")
		lib.Dirents = append(lib.Dirents, modules)
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
		FromLiteral: systemCertsPEM,
	})

	schema := "http"
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
			schema = "https"
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

	etc.Dirents = append(etc.Dirents, &FileInfo{
		Filename:    "gokr-pw.txt",
		Mode:        0400,
		FromLiteral: update.HttpPassword,
	})

	etc.Dirents = append(etc.Dirents, &FileInfo{
		Filename:    "http-port.txt",
		FromLiteral: update.HttpPort,
	})

	etc.Dirents = append(etc.Dirents, &FileInfo{
		Filename:    "https-port.txt",
		FromLiteral: update.HttpsPort,
	})

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

	p := Pack{
		Cfg:  cfg,
		Pack: packer.NewPackForHost(cfg.Hostname),
	}

	newInstallation := updateflag.NewInstallation()
	useGPT := newInstallation && !mbrOnlyWithoutGpt

	p.Pack.UsePartuuid = newInstallation
	p.Pack.UseGPTPartuuid = useGPT
	p.Pack.UseGPT = useGPT

	var (
		updateHttpClient         *http.Client
		foundMatchingCertificate bool
		updateBaseUrl            *url.URL
		target                   *updater.Target
	)

	if !updateflag.NewInstallation() {
		updateBaseUrl, err = updateflag.BaseURL(update.HttpPort, schema, update.Hostname, update.HttpPassword)
		if err != nil {
			return err
		}

		updateHttpClient, foundMatchingCertificate, err = tlsflag.GetTLSHttpClient(updateBaseUrl)
		if err != nil {
			return fmt.Errorf("getting http client by tls flag: %v", err)
		}
		done := measure.Interactively("probing https")
		remoteScheme, err := httpclient.GetRemoteScheme(updateBaseUrl)
		done("")
		if remoteScheme == "https" && !tlsflag.Insecure() {
			updateBaseUrl.Scheme = "https"
			updateflag.SetUpdate(updateBaseUrl.String())
		}

		if updateBaseUrl.Scheme != "https" && foundMatchingCertificate {
			fmt.Printf("\n")
			fmt.Printf("!!!WARNING!!! Possible SSL-Stripping detected!\n")
			fmt.Printf("Found certificate for hostname in your client configuration but the host does not offer https!\n")
			fmt.Printf("\n")
			if !tlsflag.Insecure() {
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
		p.UseGPT = target.Supports("gpt")
	}
	fmt.Printf("\n")
	fmt.Printf("Feature summary:\n")
	fmt.Printf("  use GPT: %v\n", p.UseGPT)
	fmt.Printf("  use PARTUUID: %v\n", p.UsePartuuid)
	fmt.Printf("  use GPT PARTUUID: %v\n", p.UseGPTPartuuid)

	// Determine where to write the boot and root images to.
	var (
		isDev                    bool
		tmpBoot, tmpRoot, tmpMBR *os.File
		bootSize, rootSize       int64
	)
	switch {
	case cfg.InternalCompatibilityFlags.Overwrite != "":
		st, err := os.Stat(cfg.InternalCompatibilityFlags.Overwrite)
		if err != nil && !os.IsNotExist(err) {
			return err
		}

		isDev = err == nil && st.Mode()&os.ModeDevice == os.ModeDevice

		if isDev {
			if err := p.overwriteDevice(cfg.InternalCompatibilityFlags.Overwrite, root, rootDeviceFiles); err != nil {
				return err
			}
			fmt.Printf("To boot gokrazy, plug the SD card into a supported device (see https://gokrazy.org/platforms/)\n")
			fmt.Printf("\n")
		} else {
			lower := 1200*MB + 8192

			if cfg.InternalCompatibilityFlags.TargetStorageBytes == 0 {
				return fmt.Errorf("-target_storage_bytes is required (e.g. -target_storage_bytes=%d) when using -overwrite with a file", lower)
			}
			if cfg.InternalCompatibilityFlags.TargetStorageBytes%512 != 0 {
				return fmt.Errorf("-target_storage_bytes must be a multiple of 512 (sector size), use e.g. %d", lower)
			}
			if cfg.InternalCompatibilityFlags.TargetStorageBytes < lower {
				return fmt.Errorf("-target_storage_bytes must be at least %d (for boot + 2 root file systems + 100 MB /perm)", lower)
			}

			bootSize, rootSize, err = p.overwriteFile(cfg.InternalCompatibilityFlags.Overwrite, root, rootDeviceFiles)
			if err != nil {
				return err
			}

			fmt.Printf("To boot gokrazy, copy %s to an SD card and plug it into a supported device (see https://gokrazy.org/platforms/)\n", cfg.InternalCompatibilityFlags.Overwrite)
			fmt.Printf("\n")
		}

	default:
		if cfg.InternalCompatibilityFlags.OverwriteBoot != "" {
			mbrfn := cfg.InternalCompatibilityFlags.OverwriteMBR
			if cfg.InternalCompatibilityFlags.OverwriteMBR == "" {
				tmpMBR, err = ioutil.TempFile("", "gokrazy")
				if err != nil {
					return err
				}
				defer os.Remove(tmpMBR.Name())
				mbrfn = tmpMBR.Name()
			}
			if err := p.writeBootFile(cfg.InternalCompatibilityFlags.OverwriteBoot, mbrfn); err != nil {
				return err
			}
		}

		if cfg.InternalCompatibilityFlags.OverwriteRoot != "" {
			if err := writeRootFile(cfg.InternalCompatibilityFlags.OverwriteRoot, root); err != nil {
				return err
			}
		}

		if cfg.InternalCompatibilityFlags.OverwriteBoot == "" && cfg.InternalCompatibilityFlags.OverwriteRoot == "" {
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

	hostPort := update.Hostname
	if hostPort == "" {
		hostPort = cfg.Hostname
	}
	if schema == "http" && update.HttpPort != "80" {
		hostPort = fmt.Sprintf("%s:%s", hostPort, update.HttpPort)
	}
	if schema == "https" && update.HttpsPort != "443" {
		hostPort = fmt.Sprintf("%s:%s", hostPort, update.HttpsPort)
	}

	fmt.Printf("\nTo interact with the device, gokrazy provides a web interface reachable at:\n")
	fmt.Printf("\n")
	fmt.Printf("\t%s://gokrazy:%s@%s/\n", schema, update.HttpPassword, hostPort)
	fmt.Printf("\n")
	fmt.Printf("In addition, the following Linux consoles are set up:\n")
	fmt.Printf("\n")
	if cfg.SerialConsole != "disabled" {
		fmt.Printf("\t1. foreground Linux console on the serial port (115200n8, pin 6, 8, 10 for GND, TX, RX), accepting input\n")
		fmt.Printf("\t2. secondary Linux framebuffer console on HDMI; shows Linux kernel message but no init system messages\n")
	} else {
		fmt.Printf("\t1. foreground Linux framebuffer console on HDMI\n")
	}

	if cfg.SerialConsole != "disabled" {
		fmt.Printf("\n")
		fmt.Printf("Use -serial_console=disabled to make gokrazy not touch the serial port,\nand instead make the framebuffer console on HDMI the foreground console\n")
	}
	fmt.Printf("\n")
	if schema == "https" {
		certObj, err := getCertificateFromString(update.CertPEM)
		if err != nil {
			return fmt.Errorf("error loading certificate: %v", err)
		} else {
			fmt.Printf("\n")
			fmt.Printf("The TLS Certificate of the gokrazy web interface is located under\n")
			fmt.Printf("\t%s\n", cfg.Meta.Path)
			fmt.Printf("The fingerprint of the Certificate is\n")
			fmt.Printf("\t%x\n", getCertificateFingerprintSHA1(certObj))
			fmt.Printf("The certificate is valid until\n")
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
			if _, err := bootFile.Seek(8192*512, io.SeekStart); err != nil {
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
			if _, err := rootFile.Seek(8192*512+100*MB, io.SeekStart); err != nil {
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
	fmt.Printf("Updating %s\n", updateBaseUrl.String())

	progctx, canc := context.WithCancel(context.Background())
	defer canc()
	prog := &progress.Reporter{}
	go prog.Report(progctx)

	// Start with the root file system because writing to the non-active
	// partition cannot break the currently running system.
	if err := updateWithProgress(prog, rootReader, target, "root file system", "root"); err != nil {
		return err
	}

	for _, rootDeviceFile := range rootDeviceFiles {
		f, err := os.Open(filepath.Join(kernelDir, rootDeviceFile.Name))
		if err != nil {
			return err
		}

		if err := updateWithProgress(
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

	if err := updateWithProgress(prog, bootReader, target, "boot file system", "boot"); err != nil {
		return err
	}

	if err := target.StreamTo("mbr", mbrReader); err != nil {
		if err == updater.ErrUpdateHandlerNotImplemented {
			log.Printf("target does not support updating MBR yet, ignoring")
		} else {
			return fmt.Errorf("updating MBR: %v", err)
		}
	}

	if cfg.InternalCompatibilityFlags.Testboot {
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

func updateWithProgress(prog *progress.Reporter, reader io.Reader, target *updater.Target, logStr string, stream string) error {
	start := time.Now()
	prog.SetStatus(fmt.Sprintf("update %s", logStr))
	prog.SetTotal(0)

	if stater, ok := reader.(interface{ Stat() (os.FileInfo, error) }); ok {
		if st, err := stater.Stat(); err == nil {
			prog.SetTotal(uint64(st.Size()))
		}
	}
	if err := target.StreamTo(stream, io.TeeReader(reader, &progress.Writer{})); err != nil {
		return fmt.Errorf("updating %s: %w", logStr, err)
	}
	duration := time.Since(start)
	transferred := progress.Reset()
	fmt.Printf("\rTransferred %s (%s) at %.2f MiB/s (total: %v)\n",
		logStr,
		humanize.Bytes(transferred),
		float64(transferred)/duration.Seconds()/1024/1024,
		duration.Round(time.Second))

	return nil
}

func Main(cfg *config.Struct) {
	if os.Getenv("GOKR_PACKER_FD") != "" { // partitioning child process
		p := Pack{
			Pack: packer.NewPackForHost(cfg.Hostname),
		}

		if _, err := p.SudoPartition(cfg.InternalCompatibilityFlags.Overwrite); err != nil {
			log.Fatal(err)
		}
		os.Exit(0)
	}

	if err := logic(cfg); err != nil {
		log.Fatal(err)
	}
}
