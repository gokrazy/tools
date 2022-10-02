package packer

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"io/ioutil"
	"log"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/gokrazy/internal/deviceconfig"
	"github.com/gokrazy/internal/fat"
	"github.com/gokrazy/internal/humanize"
	"github.com/gokrazy/internal/mbr"
	"github.com/gokrazy/internal/squashfs"
	"github.com/gokrazy/tools/internal/config"
	"github.com/gokrazy/tools/internal/measure"
	"github.com/gokrazy/tools/packer"
	"github.com/gokrazy/tools/third_party/systemd-250.5-1"
)

var (
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
)

func copyFile(fw *fat.Writer, dest string, src fs.File) error {
	st, err := src.Stat()
	if err != nil {
		return err
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

func copyFileSquash(d *squashfs.Directory, dest, src string) error {
	f, err := os.Open(src)
	if err != nil {
		return err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return err
	}
	w, err := d.File(filepath.Base(dest), st.ModTime(), st.Mode()&os.ModePerm)
	if err != nil {
		return err
	}
	if _, err := io.Copy(w, f); err != nil {
		return err
	}
	return w.Close()
}

func (p *Pack) writeCmdline(fw *fat.Writer, src string) error {
	b, err := ioutil.ReadFile(src)
	if err != nil {
		return err
	}
	cmdline := "console=tty1 "
	if *serialConsole != "disabled" && *serialConsole != "off" {
		if *serialConsole == "UART0" {
			// For backwards compatibility, treat the special value UART0 as
			// serial0,115200:
			cmdline += "console=serial0,115200 "
		} else {
			cmdline += "console=" + *serialConsole + " "
		}
	}
	cmdline += string(b)

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

func writeConfig(fw *fat.Writer, src string) error {
	b, err := ioutil.ReadFile(src)
	if err != nil {
		return err
	}
	config := string(b)
	if *serialConsole != "off" {
		config = strings.ReplaceAll(config, "enable_uart=0", "enable_uart=1")
	}
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
	}
	kernelGlobs = []string{
		"boot.scr", // u-boot script file
		"vmlinuz",
		"*.dtb",
	}
)

func (p *Pack) writeBoot(f io.Writer, mbrfilename string) error {
	fmt.Printf("\n")
	fmt.Printf("Creating boot file system\n")
	done := measure.Interactively("creating boot file system")
	fragment := ""
	defer func() {
		done(fragment)
	}()

	globs := make([]string, 0, len(firmwareGlobs)+len(kernelGlobs))
	if *firmwarePackage != "" {
		firmwareDir, err := packer.PackageDir(*firmwarePackage)
		if err != nil {
			return err
		}
		for _, glob := range firmwareGlobs {
			globs = append(globs, filepath.Join(firmwareDir, glob))
		}
	}
	var eepromDir string
	if *eepromPackage != "" {
		var err error
		eepromDir, err = packer.PackageDir(*eepromPackage)
		if err != nil {
			return err
		}
	}
	kernelDir, err := packer.PackageDir(*kernelPackage)
	if err != nil {
		return err
	}

	fmt.Printf("\nKernel directory: %s\n", kernelDir)
	for _, glob := range kernelGlobs {
		globs = append(globs, filepath.Join(kernelDir, glob))
	}

	bufw := bufio.NewWriter(f)
	fw, err := fat.NewWriter(bufw)
	if err != nil {
		return err
	}
	for _, pattern := range globs {
		matches, err := filepath.Glob(pattern)
		if err != nil {
			return err
		}
		for _, m := range matches {
			src, err := os.Open(m)
			if err != nil {
				return err
			}
			if err := copyFile(fw, "/"+filepath.Base(m), src); err != nil {
				return err
			}
		}
	}

	// EEPROM update procedure. See also:
	// https://news.ycombinator.com/item?id=21674550
	writeEepromUpdateFile := func(globPattern, target string) error {
		matches, err := filepath.Glob(globPattern)
		if err != nil {
			return err
		}
		if len(matches) == 0 {
			return fmt.Errorf("invalid -eeprom_package: no files matching %s", filepath.Base(globPattern))
		}

		// Select the EEPROM file that sorts last.
		// This corresponds to most recent for the pieeprom-*.bin files,
		// which contain the date in yyyy-mm-dd format.
		sort.Sort(sort.Reverse(sort.StringSlice(matches)))

		f, err := os.Open(matches[0])
		if err != nil {
			return err
		}
		defer f.Close()
		st, err := f.Stat()
		if err != nil {
			return err
		}
		// Copy the EEPROM file into the image and calculate its SHA256 hash
		// while doing so:
		w, err := fw.File(target, st.ModTime())
		if err != nil {
			return err
		}
		h := sha256.New()
		if _, err := io.Copy(w, io.TeeReader(f, h)); err != nil {
			return err
		}

		if filepath.Base(target) == "recovery.bin" {
			fmt.Printf("  recovery.bin\n")
			// No signature required for recovery.bin itself.
			return nil
		}
		fmt.Printf("  %s (sig %s)\n", filepath.Base(target), shortenSHA256(h.Sum(nil)))

		// Include the SHA256 hash in the image in an accompanying .sig file:
		sigFn := target
		ext := filepath.Ext(sigFn)
		if ext == "" {
			return fmt.Errorf("BUG: cannot derive signature file name from matches[0]=%q", matches[0])
		}
		sigFn = strings.TrimSuffix(sigFn, ext) + ".sig"
		w, err = fw.File(sigFn, st.ModTime())
		if err != nil {
			return err
		}
		_, err = fmt.Fprintf(w, "%x\n", h.Sum(nil))
		return err
	}
	if eepromDir != "" {
		fmt.Printf("EEPROM update summary:\n")
		if err := writeEepromUpdateFile(filepath.Join(eepromDir, "pieeprom-*.bin"), "/pieeprom.upd"); err != nil {
			return err
		}
		if err := writeEepromUpdateFile(filepath.Join(eepromDir, "recovery.bin"), "/recovery.bin"); err != nil {
			return err
		}
		if err := writeEepromUpdateFile(filepath.Join(eepromDir, "vl805-*.bin"), "/vl805.bin"); err != nil {
			return err
		}
	}

	if err := p.writeCmdline(fw, filepath.Join(kernelDir, "cmdline.txt")); err != nil {
		return err
	}

	if err := writeConfig(fw, filepath.Join(kernelDir, "config.txt")); err != nil {
		return err
	}

	if p.UseGPTPartuuid {
		srcX86, err := systemd.SystemdBootX64.Open("systemd-bootx64.efi")
		if err != nil {
			return err
		}
		if err := copyFile(fw, "/EFI/BOOT/BOOTX64.EFI", srcX86); err != nil {
			return err
		}

		srcAA86, err := systemd.SystemdBootAA64.Open("systemd-bootaa64.efi")
		if err != nil {
			return err
		}
		if err := copyFile(fw, "/EFI/BOOT/BOOTAA64.EFI", srcAA86); err != nil {
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
		if err := writeMBR(f.(io.ReadSeeker), fmbr, p.Partuuid); err != nil {
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

func findBins(cfg *config.Struct, buildEnv *packer.BuildEnv, bindir string) (*FileInfo, error) {
	result := FileInfo{Filename: ""}

	// TODO: doing all three packer.MainPackages calls concurrently hides go
	// module proxy latency

	gokrazyMainPkgs, err := buildEnv.MainPackages(cfg.InternalCompatibilityFlags.GokrazyPackages)
	if err != nil {
		return nil, err
	}
	gokrazy := FileInfo{Filename: "gokrazy"}
	for _, pkg := range gokrazyMainPkgs {
		binPath := filepath.Join(bindir, pkg.Basename())
		fileIsELFOrFatal(binPath)
		gokrazy.Dirents = append(gokrazy.Dirents, &FileInfo{
			Filename: pkg.Basename(),
			FromHost: binPath,
		})
	}

	if cfg.InternalCompatibilityFlags.InitPkg != "" {
		initMainPkgs, err := buildEnv.MainPackages([]string{cfg.InternalCompatibilityFlags.InitPkg})
		if err != nil {
			return nil, err
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
		}
	}
	result.Dirents = append(result.Dirents, &gokrazy)

	mainPkgs, err := buildEnv.MainPackages(cfg.Packages)
	if err != nil {
		return nil, err
	}
	user := FileInfo{Filename: "user"}
	for _, pkg := range mainPkgs {
		binPath := filepath.Join(bindir, pkg.Basename())
		fileIsELFOrFatal(binPath)
		user.Dirents = append(user.Dirents, &FileInfo{
			Filename: pkg.Basename(),
			FromHost: binPath,
		})
	}
	result.Dirents = append(result.Dirents, &user)
	return &result, nil
}

func writeFileInfo(dir *squashfs.Directory, fi *FileInfo) error {
	if fi.FromHost != "" { // copy a regular file
		return copyFileSquash(dir, fi.Filename, fi.FromHost)
	}
	if fi.FromLiteral != "" { // write a regular file
		mode := fi.Mode
		if mode == 0 {
			mode = 0444
		}
		w, err := dir.File(fi.Filename, time.Now(), mode)
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

func writeRoot(f io.WriteSeeker, root *FileInfo) error {
	fmt.Printf("\n")
	fmt.Printf("Creating root file system\n")
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

func writeRootDeviceFiles(f io.WriteSeeker, rootDeviceFiles []deviceconfig.RootFile) error {
	kernelDir, err := packer.PackageDir(*kernelPackage)
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

func writeMBR(f io.ReadSeeker, fw io.WriteSeeker, partuuid uint32) error {
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
	vmlinuzLba := uint32((vmlinuzOffset / 512) + 8192)
	cmdlineTxtLba := uint32((cmdlineOffset / 512) + 8192)

	fmt.Printf("MBR summary:\n")
	fmt.Printf("  LBAs: vmlinuz=%d cmdline.txt=%d\n", vmlinuzLba, cmdlineTxtLba)
	fmt.Printf("  PARTUUID: %08x\n", partuuid)
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
