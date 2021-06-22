package main

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

	"github.com/gokrazy/internal/fat"
	"github.com/gokrazy/internal/mbr"
	"github.com/gokrazy/internal/squashfs"
	"github.com/gokrazy/tools/third_party/systemd-248.3-2"
)

var (
	serialConsole = flag.String("serial_console",
		"ttyAMA0,115200",
		`"ttyAMA0,115200" enables UART0 as a serial console, "disabled" allows applications to use UART0 instead`)

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

func (p *pack) writeCmdline(fw *fat.Writer, src string) error {
	b, err := ioutil.ReadFile(src)
	if err != nil {
		return err
	}
	var cmdline string
	if *serialConsole != "disabled" {
		if *serialConsole == "UART0" {
			// For backwards compatibility, treat the special value UART0 as
			// ttyAMA0,115200:
			cmdline = "console=ttyAMA0,115200 " + string(b)
		} else {
			cmdline = "console=" + *serialConsole + " " + string(b)
		}
	} else {
		cmdline = string(b)
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

func writeConfig(fw *fat.Writer, src string) error {
	b, err := ioutil.ReadFile(src)
	if err != nil {
		return err
	}
	config := string(b)
	if *serialConsole != "disabled" {
		config = strings.ReplaceAll(config, "enable_uart=0", "enable_uart=1")
	}
	w, err := fw.File("/config.txt", time.Now())
	if err != nil {
		return err
	}
	_, err = w.Write([]byte(config))
	return err
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
		"vmlinuz",
		"*.dtb",
	}
)

func (p *pack) writeBoot(f io.Writer, mbrfilename string) error {
	log.Printf("writing boot file system")
	globs := make([]string, 0, len(firmwareGlobs)+len(kernelGlobs))
	firmwareDir, err := packageDir(*firmwarePackage)
	if err != nil {
		return err
	}
	for _, glob := range firmwareGlobs {
		globs = append(globs, filepath.Join(firmwareDir, glob))
	}
	var eepromDir string
	if *eepromPackage != "" {
		var err error
		eepromDir, err = packageDir(*eepromPackage)
		if err != nil {
			return err
		}
	}
	kernelDir, err := packageDir(*kernelPackage)
	if err != nil {
		return err
	}
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
			log.Printf("writing EEPROM update file recovery.bin")
			// No signature required for recovery.bin itself.
			return nil
		}
		log.Printf("writing EEPROM update file %s (sig %x)", filepath.Base(target), h.Sum(nil))

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
		src, err := systemd.SystemdBootX64.Open("systemd-bootx64.efi")
		if err != nil {
			return err
		}
		if err := copyFile(fw, "/EFI/BOOT/BOOTX64.EFI", src); err != nil {
			return err
		}
	}

	if err := fw.Flush(); err != nil {
		return err
	}
	if err := bufw.Flush(); err != nil {
		return err
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

type fileInfo struct {
	filename string
	mode     os.FileMode

	fromHost    string
	fromLiteral string
	symlinkDest string

	dirents []*fileInfo
}

func (fi *fileInfo) isFile() bool {
	return fi.fromHost != "" || fi.fromLiteral != ""
}

func (fi *fileInfo) pathList() (paths []string) {
	for _, ent := range fi.dirents {
		if ent.isFile() {
			paths = append(paths, ent.filename)
			continue
		}

		for _, e := range ent.pathList() {
			paths = append(paths, path.Join(ent.filename, e))
		}
	}
	return
}

func (fi *fileInfo) combine(fi2 *fileInfo) error {
	for _, ent2 := range fi2.dirents {
		// get existing file info
		var f *fileInfo
		for _, ent := range fi.dirents {
			if ent.filename == ent2.filename {
				f = ent
				break
			}
		}

		// if not found add complete subtree directly
		if f == nil {
			fi.dirents = append(fi.dirents, ent2)
			continue
		}

		// file overwrite is not supported -> return error
		if f.isFile() || ent2.isFile() {
			return fmt.Errorf("file already exist: %s", ent2.filename)
		}

		if err := f.combine(ent2); err != nil {
			return err
		}
	}
	return nil
}

func (fi *fileInfo) mustFindDirent(path string) *fileInfo {
	for _, ent := range fi.dirents {
		// TODO: split path into components and compare piecemeal
		if ent.filename == path {
			return ent
		}
	}
	log.Panicf("mustFindDirent(%q) did not find directory entry", path)
	return nil
}

func findBins(bindir string) (*fileInfo, error) {
	result := fileInfo{filename: ""}

	gokrazyMainPkgs, err := mainPackages(gokrazyPkgs)
	if err != nil {
		return nil, err
	}
	gokrazy := fileInfo{filename: "gokrazy"}
	for _, pkg := range gokrazyMainPkgs {
		gokrazy.dirents = append(gokrazy.dirents, &fileInfo{
			filename: pkg.Basename(),
			fromHost: filepath.Join(bindir, pkg.Basename()),
		})
	}

	if *initPkg != "" {
		initMainPkgs, err := mainPackages([]string{*initPkg})
		if err != nil {
			return nil, err
		}
		for _, pkg := range initMainPkgs {
			if got, want := pkg.Basename(), "init"; got != want {
				log.Printf("Error: -init_pkg=%q produced unexpected binary name: got %q, want %q", *initPkg, got, want)
				continue
			}
			gokrazy.dirents = append(gokrazy.dirents, &fileInfo{
				filename: pkg.Basename(),
				fromHost: filepath.Join(bindir, pkg.Basename()),
			})
		}
	}
	result.dirents = append(result.dirents, &gokrazy)

	mainPkgs, err := mainPackages(flag.Args())
	if err != nil {
		return nil, err
	}
	user := fileInfo{filename: "user"}
	for _, pkg := range mainPkgs {
		user.dirents = append(user.dirents, &fileInfo{
			filename: pkg.Basename(),
			fromHost: filepath.Join(bindir, pkg.Basename()),
		})
	}
	result.dirents = append(result.dirents, &user)
	return &result, nil
}

func writeFileInfo(dir *squashfs.Directory, fi *fileInfo) error {
	if fi.fromHost != "" { // copy a regular file
		return copyFileSquash(dir, fi.filename, fi.fromHost)
	}
	if fi.fromLiteral != "" { // write a regular file
		mode := fi.mode
		if mode == 0 {
			mode = 0444
		}
		w, err := dir.File(fi.filename, time.Now(), mode)
		if err != nil {
			return err
		}
		if _, err := w.Write([]byte(fi.fromLiteral)); err != nil {
			return err
		}
		return w.Close()
	}

	if fi.symlinkDest != "" { // create a symlink
		return dir.Symlink(fi.symlinkDest, fi.filename, time.Now(), 0444)
	}
	// subdir
	var d *squashfs.Directory
	if fi.filename == "" { // root
		d = dir
	} else {
		d = dir.Directory(fi.filename, time.Now())
	}
	sort.Slice(fi.dirents, func(i, j int) bool {
		return fi.dirents[i].filename < fi.dirents[j].filename
	})
	for _, ent := range fi.dirents {
		if err := writeFileInfo(d, ent); err != nil {
			return err
		}
	}
	return d.Flush()
}

func writeRoot(f io.WriteSeeker, root *fileInfo) error {
	log.Printf("writing root file system")
	fw, err := squashfs.NewWriter(f, time.Now())
	if err != nil {
		return err
	}

	if err := writeFileInfo(fw.Root, root); err != nil {
		return err
	}

	return fw.Flush()
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

	log.Printf("writing MBR (LBAs: vmlinuz=%d, cmdline.txt=%d, PARTUUID=%08x)", vmlinuzLba, cmdlineTxtLba, partuuid)
	mbr := mbr.Configure(vmlinuzLba, cmdlineTxtLba, partuuid)
	if _, err := fw.Write(mbr[:]); err != nil {
		return err
	}

	return nil
}

// getDuplication between the two given filesystems
func getDuplication(fiA, fiB *fileInfo) (paths []string) {
	allPaths := append(fiA.pathList(), fiB.pathList()...)
	checkMap := make(map[string]bool, len(allPaths))
	for _, p := range allPaths {
		if _, ok := checkMap[p]; ok {
			paths = append(paths, p)
		}
		checkMap[p] = true
	}
	return
}
