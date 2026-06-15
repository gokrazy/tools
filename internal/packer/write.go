package packer

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"fmt"
	"io"
	"io/fs"
	"log"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/gokrazy/internal/config"
	"github.com/gokrazy/internal/deviceconfig"
	"github.com/gokrazy/internal/fat"
	"github.com/gokrazy/internal/humanize"
	"github.com/gokrazy/internal/mbr"
	"github.com/gokrazy/internal/squashfs"
	"github.com/gokrazy/tools/internal/measure"
	"github.com/gokrazy/tools/packer"
	"github.com/gokrazy/tools/third_party/systemd-250.5-1"
)

func copyFile(fw *fat.Writer, dest string, src fs.File, srcName string) error {
	st, err := src.Stat()
	if err != nil {
		return err
	}
	exists, err := fw.Exists(dest)
	if err != nil {
		return err
	}
	if exists {
		return fmt.Errorf("copyFile(%s, %s): file already exists", dest, srcName)
	}
	w, err := fw.File(dest, st.ModTime())
	if err != nil {
		return err
	}
	if _, err := io.Copy(w, src); err != nil {
		return err
	}
	return src.Close()
}

func copyFileSquash(d *squashfs.Directory, dest, src string, xattrs map[string][]byte) error {
	f, err := os.Open(src)
	if err != nil {
		return err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return err
	}
	w, err := d.File(filepath.Base(dest), st.ModTime(), st.Mode()&os.ModePerm, xattrs)
	if err != nil {
		return err
	}
	if _, err := io.Copy(w, f); err != nil {
		return err
	}
	return w.Close()
}

func (p *Pack) writeCmdline(fw *fat.Writer, src string) error {
	log := p.Env.Logger()

	b, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	cmdline := "console=tty1 "
	serialConsole := p.Cfg.SerialConsoleOrDefault()
	if serialConsole != "disabled" && serialConsole != "off" {
		if serialConsole == "UART0" {
			// For backwards compatibility, treat the special value UART0 as
			// serial0,115200:
			cmdline += "console=serial0,115200 "
		} else {
			cmdline += "console=" + serialConsole + " "
		}
	}
	cmdline += strings.TrimSpace(string(b))

	if args := p.Cfg.KernelExtraArgs; len(args) > 0 {
		cmdline += " " + strings.Join(args, " ")
	}

	// TODO: change {gokrazy,rtr7}/kernel/cmdline.txt to contain a dummy PARTUUID=
	if p.ModifyCmdlineRoot() {
		root := "root=" + p.Root()
		cmdline = strings.ReplaceAll(cmdline, "root=/dev/mmcblk0p2", root)
		cmdline = strings.ReplaceAll(cmdline, "root=/dev/sda2", root)
	} else {
		log.Printf("(not using PARTUUID= in cmdline.txt yet)")
	}

	// Pad the kernel command line with enough whitespace that can be used for
	// in-place file overwrites to add additional command line flags for the
	// gokrazy update process:
	const pad = 64
	padded := append([]byte(cmdline), bytes.Repeat([]byte{' '}, pad)...)

	w, err := fw.File("/cmdline.txt", time.Now())
	if err != nil {
		return err
	}
	if _, err := w.Write(padded); err != nil {
		return err
	}

	if p.UseGPTPartuuid {
		// In addition to the cmdline.txt for the Raspberry Pi bootloader, also
		// write a systemd-boot entries configuration file as per
		// https://systemd.io/BOOT_LOADER_SPECIFICATION/
		w, err = fw.File("/loader/entries/gokrazy.conf", time.Now())
		if err != nil {
			return err
		}
		fmt.Fprintf(w, `title gokrazy
linux /vmlinuz
`)
		if _, err := w.Write(append([]byte("options "), padded...)); err != nil {
			return err
		}
	}

	return nil
}

func (p *Pack) writeConfig(fw *fat.Writer, src string) error {
	b, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	config := string(b)
	if p.Cfg.SerialConsoleOrDefault() != "off" {
		config = strings.ReplaceAll(config, "enable_uart=0", "enable_uart=1")
	}
	config += "\n"
	config += strings.Join(p.Cfg.BootloaderExtraLines, "\n")
	w, err := fw.File("/config.txt", time.Now())
	if err != nil {
		return err
	}
	_, err = w.Write([]byte(config))
	return err
}

func shortenSHA256(sum []byte) string {
	hash := fmt.Sprintf("%x", sum)
	if len(hash) > 10 {
		return hash[:10]
	}
	return hash
}

var (
	firmwareGlobs = []string{
		"*.bin",
		"*.dat",
		"*.elf",
		"*.upd",
		"*.sig",
		"overlays/*.dtbo",
	}
	kernelGlobs = []string{
		"boot.scr", // u-boot script file
		"vmlinuz",
		"*.dtb",
		"overlays/*.dtbo",
		"overlays/overlay_map.dtb",
	}
)

func (p *Pack) copyGlobsToBoot(fw *fat.Writer, srcDir string, globs []string) error {
	for _, pattern := range globs {
		matches, err := filepath.Glob(filepath.Join(srcDir, pattern))
		if err != nil {
			return err
		}
		for _, m := range matches {
			src, err := os.Open(m)
			if err != nil {
				return err
			}
			relPath, err := filepath.Rel(srcDir, m)
			if err != nil {
				return err
			}
			if err := copyFile(fw, "/"+relPath, src, m); err != nil {
				return err
			}
		}
	}
	return nil
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

func (p *Pack) writeBoot(f io.Writer, mbrfilename string) error {
	log := p.Env.Logger()
	log.Printf("")
	log.Printf("Creating boot file system")
	done := measure.Interactively("creating boot file system")
	fragment := ""
	defer func() {
		done(fragment)
	}()

	var firmwareDir string
	if fw := p.Cfg.FirmwarePackageOrDefault(); fw != "" {
		var err error
		firmwareDir, err = packer.PackageDir(fw)
		if err != nil {
			return err
		}
	}
	var eepromDir string
	if eeprom := p.Cfg.EEPROMPackageOrDefault(); eeprom != "" {
		var err error
		eepromDir, err = packer.PackageDir(eeprom)
		if err != nil {
			return err
		}
	}
	kernelDir, err := packer.PackageDir(p.Cfg.KernelPackageOrDefault())
	if err != nil {
		return err
	}

	log.Printf("")
	log.Printf("Kernel directory: %s", kernelDir)

	bufw := bufio.NewWriter(f)
	fw, err := fat.NewWriter(bufw)
	if err != nil {
		return err
	}

	err = p.copyGlobsToBoot(fw, kernelDir, kernelGlobs)
	if err != nil {
		return err
	}

	if firmwareDir != "" {
		err = p.copyGlobsToBoot(fw, firmwareDir, firmwareGlobs)
		if err != nil {
			return err
		}
	}

	// matches must be non-empty
	bestMatch := func(matches []string) string {
		// Select the EEPROM file that sorts last.
		// This corresponds to most recent for the pieeprom-*.bin files,
		// which contain the date in yyyy-mm-dd format.
		sort.Sort(sort.Reverse(sort.StringSlice(matches)))
		return matches[0]
	}
	// EEPROM update procedure. See also:
	// https://news.ycombinator.com/item?id=21674550
	writeEepromUpdate := func(target string, modTime time.Time, r io.Reader) (sig string, _ error) {
		// Copy the EEPROM file into the image and calculate its SHA256 hash
		// while doing so:
		w, err := fw.File(target, modTime)
		if err != nil {
			return "", err
		}
		h := sha256.New()
		if _, err := io.Copy(w, io.TeeReader(r, h)); err != nil {
			return "", err
		}

		if base := filepath.Base(target); base == "recovery.bin" || base == "RECOVERY.000" {
			log.Printf("  %s", base)
			// No signature required for recovery.bin itself.
			return "", nil
		}
		log.Printf("  %s (sig %s)", filepath.Base(target), shortenSHA256(h.Sum(nil)))

		// Include the SHA256 hash in the image in an accompanying .sig file:
		sigFn := target
		ext := filepath.Ext(sigFn)
		if ext == "" {
			return "", fmt.Errorf("BUG: cannot derive signature file name from target=%q", target)
		}
		sigFn = strings.TrimSuffix(sigFn, ext) + ".sig"
		w, err = fw.File(sigFn, modTime)
		if err != nil {
			return "", err
		}
		_, err = fmt.Fprintf(w, "%x\n", h.Sum(nil))
		if err != nil {
			return "", err
		}
		_, err = fmt.Fprintf(w, "ts: %d\n", modTime.Unix())
		return fmt.Sprintf("%x", h.Sum(nil)), err
	}
	writeEepromUpdateFile := func(matches []string, target string) (sig string, _ error) {
		f, err := os.Open(bestMatch(matches))
		if err != nil {
			return "", err
		}
		defer f.Close()
		st, err := f.Stat()
		if err != nil {
			return "", err
		}
		return writeEepromUpdate(target, st.ModTime(), f)
	}

	var pieSig string
	if eepromDir != "" {
		log.Printf("EEPROM directory: %s", eepromDir)
		log.Printf("(gokrazy config mtime: %v)", p.Cfg.Meta.LastModified)
		log.Printf("EEPROM update summary:")
		eepromGlob := filepath.Join(eepromDir, "pieeprom-*.bin")
		eepromMatches, err := filepath.Glob(eepromGlob)
		if err != nil {
			return err
		}
		if len(eepromMatches) == 0 {
			return fmt.Errorf("invalid -eeprom_package: no files matching %s", filepath.Base(eepromGlob))
		}
		if ee := p.Cfg.BootloaderExtraEEPROM; len(ee) > 0 {
			updated, err := applyExtraEEPROM(bestMatch(eepromMatches), ee)
			if err != nil {
				return err
			}
			pieSig, err = writeEepromUpdate("/pieeprom.upd", p.Cfg.Meta.LastModified, bytes.NewReader(updated))
			if err != nil {
				return err
			}
		} else {
			pieSig, err = writeEepromUpdateFile(eepromMatches, "/pieeprom.upd")
			if err != nil {
				return err
			}
		}

		vl805Glob := filepath.Join(eepromDir, "vl805-*.bin")
		vl805Matches, err := filepath.Glob(vl805Glob)
		if err != nil {
			return err
		}
		var vlSig string
		if len(vl805Matches) > 0 {
			vlSig, err = writeEepromUpdateFile(vl805Matches, "/vl805.bin")
			if err != nil {
				return err
			}
		}
		targetFilename := "/recovery.bin"
		if pieSig == p.ExistingEEPROM.PieepromSHA256 &&
			vlSig == p.ExistingEEPROM.VL805SHA256 {
			log.Printf("  installing recovery.bin as RECOVERY.000 (EEPROM already up-to-date)")
			targetFilename = "/RECOVERY.000"
		}
		if _, err := writeEepromUpdateFile([]string{filepath.Join(eepromDir, "recovery.bin")}, targetFilename); err != nil {
			return err
		}
	}

	if err := p.writeCmdline(fw, filepath.Join(kernelDir, "cmdline.txt")); err != nil {
		return err
	}

	if err := p.writeConfig(fw, filepath.Join(kernelDir, "config.txt")); err != nil {
		return err
	}

	if p.UseGPTPartuuid {
		srcX86, err := systemd.SystemdBootX64.Open("systemd-bootx64.efi")
		if err != nil {
			return err
		}
		if err := copyFile(fw, "/EFI/BOOT/BOOTX64.EFI", srcX86, "<embedded>"); err != nil {
			return err
		}

		srcAA86, err := systemd.SystemdBootAA64.Open("systemd-bootaa64.efi")
		if err != nil {
			return err
		}
		if err := copyFile(fw, "/EFI/BOOT/BOOTAA64.EFI", srcAA86, "<embedded>"); err != nil {
			return err
		}
	}

	if err := fw.Flush(); err != nil {
		return err
	}
	if err := bufw.Flush(); err != nil {
		return err
	}
	if seeker, ok := f.(io.Seeker); ok {
		off, err := seeker.Seek(0, io.SeekCurrent)
		if err != nil {
			return err
		}
		fragment = ", " + humanize.Bytes(uint64(off))
	}
	if mbrfilename != "" {
		if _, ok := f.(io.ReadSeeker); !ok {
			return fmt.Errorf("BUG: f does not implement io.ReadSeeker")
		}
		fmbr, err := os.OpenFile(mbrfilename, os.O_RDWR|os.O_CREATE, 0600)
		if err != nil {
			return err
		}
		defer fmbr.Close()
		if err := p.writeMBR(p.FirstPartitionOffsetSectors, f.(io.ReadSeeker), fmbr, p.Partuuid); err != nil {
			return err
		}
		if err := fmbr.Close(); err != nil {
			return err
		}
	}
	return nil
}

type FileInfo struct {
	Filename string
	Mode     os.FileMode

	FromHost    string
	FromLiteral string
	SymlinkDest string
	Xattrs      map[string][]byte

	Dirents []*FileInfo
}

func (fi *FileInfo) isFile() bool {
	return fi.FromHost != "" || fi.FromLiteral != ""
}

func (fi *FileInfo) pathList() (paths []string) {
	for _, ent := range fi.Dirents {
		if ent.isFile() {
			paths = append(paths, ent.Filename)
			continue
		}

		for _, e := range ent.pathList() {
			paths = append(paths, path.Join(ent.Filename, e))
		}
	}
	return paths
}

func (fi *FileInfo) combine(fi2 *FileInfo) error {
	for _, ent2 := range fi2.Dirents {
		// get existing file info
		var f *FileInfo
		for _, ent := range fi.Dirents {
			if ent.Filename == ent2.Filename {
				f = ent
				break
			}
		}

		// if not found add complete subtree directly
		if f == nil {
			fi.Dirents = append(fi.Dirents, ent2)
			continue
		}

		// file overwrite is not supported -> return error
		if f.isFile() || ent2.isFile() {
			return fmt.Errorf("file already exist: %s", ent2.Filename)
		}

		if err := f.combine(ent2); err != nil {
			return err
		}
	}
	return nil
}

func (fi *FileInfo) mustFindDirent(path string) *FileInfo {
	for _, ent := range fi.Dirents {
		// TODO: split path into components and compare piecemeal
		if ent.Filename == path {
			return ent
		}
	}
	log.Panicf("mustFindDirent(%q) did not find directory entry", path)
	return nil
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

type foundBin struct {
	gokrazyPath string
	hostPath    string
}

func findBins(cfg *config.Struct, buildEnv *packer.BuildEnv, bindir string, basenames map[string]string, xattrs map[string]map[string][]byte) (*FileInfo, []foundBin, error) {
	var found []foundBin
	result := FileInfo{Filename: ""}

	// TODO: doing all three packer.MainPackages calls concurrently hides go
	// module proxy latency

	gokrazyMainPkgs, err := buildEnv.MainPackages(cfg.GokrazyPackagesOrDefault())
	if err != nil {
		return nil, nil, err
	}
	gokrazy := FileInfo{Filename: "gokrazy"}
	for _, pkg := range gokrazyMainPkgs {
		binPath := filepath.Join(bindir, pkg.Basename())
		fileIsELFOrFatal(binPath)
		gokrazy.Dirents = append(gokrazy.Dirents, &FileInfo{
			Filename: pkg.Basename(),
			FromHost: binPath,
		})
		found = append(found, foundBin{
			gokrazyPath: "/gokrazy/" + pkg.Basename(),
			hostPath:    binPath,
		})
	}

	if cfg.InternalCompatibilityFlags.InitPkg != "" {
		initMainPkgs, err := buildEnv.MainPackages([]string{cfg.InternalCompatibilityFlags.InitPkg})
		if err != nil {
			return nil, nil, err
		}
		for _, pkg := range initMainPkgs {
			if got, want := pkg.Basename(), "init"; got != want {
				log.Printf("Error: -init_pkg=%q produced unexpected binary name: got %q, want %q", cfg.InternalCompatibilityFlags.InitPkg, got, want)
				continue
			}
			binPath := filepath.Join(bindir, pkg.Basename())
			fileIsELFOrFatal(binPath)
			gokrazy.Dirents = append(gokrazy.Dirents, &FileInfo{
				Filename: pkg.Basename(),
				FromHost: binPath,
			})
			found = append(found, foundBin{
				gokrazyPath: "/gokrazy/init",
				hostPath:    binPath,
			})
		}
	}
	result.Dirents = append(result.Dirents, &gokrazy)

	mainPkgs, err := buildEnv.MainPackages(cfg.Packages)
	if err != nil {
		return nil, nil, err
	}
	user := FileInfo{Filename: "user"}
	for _, pkg := range mainPkgs {
		xattr := make(map[string][]byte)
		basename := pkg.Basename()
		if basenameOverride, ok := basenames[pkg.ImportPath]; ok {
			basename = basenameOverride
		}
		if xattrsOverride, ok := xattrs[pkg.ImportPath]; ok {
			xattr = xattrsOverride
		}
		binPath := filepath.Join(bindir, basename)
		fileIsELFOrFatal(binPath)
		user.Dirents = append(user.Dirents, &FileInfo{
			Filename: basename,
			FromHost: binPath,
			Xattrs:   xattr,
		})
		found = append(found, foundBin{
			gokrazyPath: "/user/" + basename,
			hostPath:    binPath,
		})
	}
	result.Dirents = append(result.Dirents, &user)
	return &result, found, nil
}

func writeFileInfo(dir *squashfs.Directory, fi *FileInfo) error {
	if fi.FromHost != "" { // copy a regular file
		return copyFileSquash(dir, fi.Filename, fi.FromHost, fi.Xattrs)
	}
	if fi.FromLiteral != "" { // write a regular file
		mode := fi.Mode
		if mode == 0 {
			mode = 0444
		}
		w, err := dir.File(fi.Filename, time.Now(), mode, fi.Xattrs)
		if err != nil {
			return err
		}
		if _, err := w.Write([]byte(fi.FromLiteral)); err != nil {
			return err
		}
		return w.Close()
	}

	if fi.SymlinkDest != "" { // create a symlink
		return dir.Symlink(fi.SymlinkDest, fi.Filename, time.Now(), 0444)
	}
	// subdir
	var d *squashfs.Directory
	if fi.Filename == "" { // root
		d = dir
	} else {
		d = dir.Directory(fi.Filename, time.Now())
	}
	sort.Slice(fi.Dirents, func(i, j int) bool {
		return fi.Dirents[i].Filename < fi.Dirents[j].Filename
	})
	for _, ent := range fi.Dirents {
		if err := writeFileInfo(d, ent); err != nil {
			return err
		}
	}
	return d.Flush()
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

func (p *Pack) writeRoot(f io.WriteSeeker, root *FileInfo) error {
	log := p.Env.Logger()

	log.Printf("")
	log.Printf("Creating root file system")
	done := measure.Interactively("creating root file system")
	defer func() {
		done("")
	}()

	// TODO: make fw.Flush() report the size of the root fs

	fw, err := squashfs.NewWriter(f, time.Now())
	if err != nil {
		return err
	}

	if err := writeFileInfo(fw.Root, root); err != nil {
		return err
	}

	return fw.Flush()
}

func (p *Pack) writeRootDeviceFiles(f io.WriteSeeker, rootDeviceFiles []deviceconfig.RootFile) error {
	kernelDir, err := packer.PackageDir(p.Cfg.KernelPackageOrDefault())
	if err != nil {
		return err
	}

	for _, rootFile := range rootDeviceFiles {
		if _, err := f.Seek(rootFile.Offset, io.SeekStart); err != nil {
			return err
		}

		source, err := os.Open(filepath.Join(kernelDir, rootFile.Name))
		if err != nil {
			return err
		}
		if _, err := io.Copy(f, source); err != nil {
			return err
		}
		_ = source.Close()
	}

	return nil
}

func (p *Pack) writeMBR(firstPartitionOffsetSectors int64, f io.ReadSeeker, fw io.WriteSeeker, partuuid uint32) error {
	log := p.Env.Logger()

	rd, err := fat.NewReader(f)
	if err != nil {
		return err
	}
	vmlinuzOffset, _, err := rd.Extents("/vmlinuz")
	if err != nil {
		return err
	}
	cmdlineOffset, _, err := rd.Extents("/cmdline.txt")
	if err != nil {
		return err
	}

	if _, err := fw.Seek(0, io.SeekStart); err != nil {
		return err
	}
	vmlinuzLba := uint32((vmlinuzOffset / 512) + firstPartitionOffsetSectors)
	cmdlineTxtLba := uint32((cmdlineOffset / 512) + firstPartitionOffsetSectors)

	log.Printf("MBR summary:")
	log.Printf("  LBAs: vmlinuz=%d cmdline.txt=%d", vmlinuzLba, cmdlineTxtLba)
	log.Printf("  PARTUUID: %08x", partuuid)
	mbr := mbr.Configure(vmlinuzLba, cmdlineTxtLba, partuuid)
	if _, err := fw.Write(mbr[:]); err != nil {
		return err
	}

	return nil
}

// getDuplication between the two given filesystems
func getDuplication(fiA, fiB *FileInfo) (paths []string) {
	allPaths := append(fiA.pathList(), fiB.pathList()...)
	checkMap := make(map[string]bool, len(allPaths))
	for _, p := range allPaths {
		if _, ok := checkMap[p]; ok {
			paths = append(paths, p)
		}
		checkMap[p] = true
	}
	return paths
}
