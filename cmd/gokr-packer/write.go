package main

import (
	"bufio"
	"flag"
	"io"
	"io/ioutil"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/gokrazy/internal/fat"
)

var (
	serialConsole = flag.String("serial_console",
		"UART0",
		`"UART0" enables UART0 as a serial console, "disabled" allows applications to use UART0 instead`)
)

func copyFile(fw *fat.Writer, dest, src string) error {
	f, err := os.Open(src)
	if err != nil {
		return err
	}
	st, err := f.Stat()
	if err != nil {
		return err
	}
	w, err := fw.File(dest, st.ModTime())
	if err != nil {
		return err
	}
	if _, err := io.Copy(w, f); err != nil {
		return err
	}
	return f.Close()
}

func writeCmdline(fw *fat.Writer, src string) error {
	b, err := ioutil.ReadFile(src)
	if err != nil {
		return err
	}
	var cmdline string
	if *serialConsole == "UART0" {
		cmdline = "console=ttyAMA0,115200 " + string(b)
	} else {
		cmdline = string(b)
	}
	w, err := fw.File("/cmdline.txt", time.Now())
	if err != nil {
		return err
	}
	_, err = w.Write([]byte(cmdline))
	return err
}

func writeConfig(fw *fat.Writer, src string) error {
	b, err := ioutil.ReadFile(src)
	if err != nil {
		return err
	}
	config := string(b)
	if *serialConsole == "UART0" {
		config = strings.Replace(config, "enable_uart=0", "enable_uart=1", -1)
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
	}
	kernelGlobs = []string{
		"vmlinuz",
		"*.dtb",
	}
)

func writeBoot(f io.Writer) error {
	log.Printf("writing boot file system")
	globs := make([]string, 0, len(firmwareGlobs)+len(kernelGlobs))
	firmwareDir, err := packageDir("github.com/gokrazy/firmware")
	if err != nil {
		return err
	}
	for _, glob := range firmwareGlobs {
		globs = append(globs, filepath.Join(firmwareDir, glob))
	}
	kernelDir, err := packageDir("github.com/gokrazy/kernel")
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
			if err := copyFile(fw, "/"+filepath.Base(m), m); err != nil {
				return err
			}
		}
	}

	if err := writeCmdline(fw, filepath.Join(kernelDir, "cmdline.txt")); err != nil {
		return err
	}

	if err := writeConfig(fw, filepath.Join(kernelDir, "config.txt")); err != nil {
		return err
	}

	if err := fw.Flush(); err != nil {
		return err
	}
	return bufw.Flush()
}

var safeForFilenameRe = regexp.MustCompile(`[^a-zA-Z0-9]`)

// truncate83 truncates a filename to 8 characters plus 3 extension
// characters, as is required on FAT file systems.
func truncate83(filename string) string {
	filename = safeForFilenameRe.ReplaceAllLiteralString(filename, "")
	parts := strings.Split(filename+".", ".")
	if len(parts[0]) > 8 {
		parts[0] = parts[0][:8]
	}
	if len(parts[1]) > 3 {
		parts[1] = parts[1][:3]
	}
	if len(parts[1]) > 0 {
		return parts[0] + "." + parts[1]
	} else {
		return parts[0]
	}
}

func findBins() (map[string]string, error) {
	result := make(map[string]string)

	gokrazyMainPkgs, err := mainPackages(gokrazyPkgs)
	if err != nil {
		return nil, err
	}
	for _, target := range gokrazyMainPkgs {
		result["/gokrazy/"+truncate83(filepath.Base(target))] = target
	}

	if *initPkg != "" {
		initMainPkgs, err := mainPackages([]string{*initPkg})
		if err != nil {
			return nil, err
		}
		for _, target := range initMainPkgs {
			if got, want := filepath.Base(target), "init"; got != want {
				log.Printf("Error: -init_pkg=%q produced unexpected binary name: got %q, want %q", *initPkg, got, want)
				continue
			}
			result["/gokrazy/init"] = target
		}
	}

	mainPkgs, err := mainPackages(flag.Args())
	if err != nil {
		return nil, err
	}
	for _, target := range mainPkgs {
		result["/user/"+truncate83(filepath.Base(target))] = target
	}
	return result, nil
}

func writeRoot(f io.Writer, bins map[string]string) error {
	log.Printf("writing root file system: %v", bins)
	bufw := bufio.NewWriter(f)
	fw, err := fat.NewWriter(bufw)
	if err != nil {
		return err
	}

	for path, target := range bins {
		if strings.HasSuffix(path, "/") {
			if err := fw.Mkdir(path, time.Now()); err != nil {
				return err
			}
		} else {
			if err := copyFile(fw, path, target); err != nil {
				return err
			}
		}
	}

	w, err := fw.File("/hostname", time.Now())
	if err != nil {
		return err
	}
	if _, err := w.Write([]byte(*hostname)); err != nil {
		return err
	}

	if err := fw.Flush(); err != nil {
		return err
	}
	return bufw.Flush()
}
