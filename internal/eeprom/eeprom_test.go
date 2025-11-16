package eeprom_test

import (
	"compress/gzip"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gokrazy/tools/internal/eeprom"
)

func testAgainst(t *testing.T, verify func(eepromPath, needle string), pieeprom, bootUartNeedle string) {
	verify(pieeprom, bootUartNeedle)

	// Read + Write the EEPROM with our own code,
	// verify that bootconf.txt can still be displayed.
	img, err := os.ReadFile(pieeprom)
	if err != nil {
		t.Fatal(err)
	}
	sections, err := eeprom.Analyze(img)
	if err != nil {
		t.Fatal(err)
	}
	reassembleAndVerify := func(needle string) {
		reassembled := filepath.Join(t.TempDir(), "reassembled.bin")
		if err := os.WriteFile(reassembled, eeprom.Assemble(sections), 0644); err != nil {
			t.Fatal(err)
		}
		verify(reassembled, needle)
	}
	reassembleAndVerify(bootUartNeedle)

	bc := sections[len(sections)-1] // guaranteed to be bootconf.txt

	// Re-create the section with the existing contents.
	sections[len(sections)-1] = eeprom.FileSection(bc.Offset, bc.Filename, bc.FileContent())
	reassembleAndVerify(bootUartNeedle)

	// Change the bootconf.txt contents.
	sections[len(sections)-1] = eeprom.FileSection(bc.Offset, bc.Filename, []byte("hello world"))
	reassembleAndVerify("hello world")
}

func TestAgainst(t *testing.T) {
	const fn = "testdata/pieeprom-2025-10-17.bin"
	f, err := os.Open(fn + ".gz")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		t.Fatal(err)
	}
	bin, err := io.ReadAll(gz)
	if err != nil {
		t.Fatal(err)
	}
	pieeprom := filepath.Join(t.TempDir(), filepath.Base(fn))
	if err := os.WriteFile(pieeprom, bin, 0644); err != nil {
		t.Fatal(err)
	}

	t.Run("Infobeamer", func(t *testing.T) {
		lsBin, err := exec.LookPath("pi-eeprom-ls")
		if err != nil {
			t.Skipf("pi-eeprom-ls not available (%v)", err)
		}

		verify := func(eepromPath, needle string) {
			ls := exec.Command(lsBin, eepromPath)
			ls.Stderr = os.Stderr
			lsOut, err := ls.Output()
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(string(lsOut), needle) {
				t.Errorf("infobeamer pi-eeprom-ls output did not contain %q: \n%s", needle, string(lsOut))
			}
		}
		const bootUartNeedle = "-[ bootconf.txt ]------------------\n[all]\nBOOT_UART=1"
		testAgainst(t, verify, pieeprom, bootUartNeedle)
	})

	t.Run("Pi", func(t *testing.T) {
		configBin, err := exec.LookPath("rpi-eeprom-config")
		if err != nil {
			t.Skipf("rpi-eeprom-config not available (%v)", err)
		}

		verify := func(eepromPath, needle string) {
			extract := exec.Command(configBin, eepromPath)
			extract.Dir = t.TempDir()
			extract.Stderr = os.Stderr
			extractOut, err := extract.Output()
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(string(extractOut), needle) {
				t.Errorf("rpi-eeprom-config output did not contain %q: \n%s", needle, string(extractOut))
			}
		}
		const bootUartNeedle = "[all]\nBOOT_UART=1"
		testAgainst(t, verify, pieeprom, bootUartNeedle)
	})
}
