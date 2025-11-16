package packer

import (
	"io"
	"os"
	"strings"

	"github.com/gokrazy/tools/internal/eeprom"
)

func applyExtraEEPROM(pieeprom string, extraEEPROM []string) ([]byte, error) {
	upstream, err := os.Open(pieeprom)
	if err != nil {
		return nil, err
	}
	upstreamBin, err := io.ReadAll(upstream)
	if err != nil {
		return nil, err
	}
	sections, err := eeprom.Analyze(upstreamBin)
	if err != nil {
		return nil, err
	}
	bc := sections[len(sections)-1] // guaranteed to be bootconf.txt
	bootconfTxt := bc.FileContent()
	applied := overwriteBootconf(bootconfTxt, extraEEPROM)
	sections[len(sections)-1] = eeprom.FileSection(bc.Offset, bc.Filename, applied)
	return eeprom.Assemble(sections), nil
}

func overwriteBootconf(bootconfTxt []byte, extraEEPROM []string) []byte {
	bootconfLines := strings.Split(strings.TrimSpace(string(bootconfTxt)), "\n")
	// For each property, there will be an entry from property to full line
	extraByProp := make(map[string]string)
	for _, line := range extraEEPROM {
		prop, _, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		extraByProp[prop] = line
	}
	var out strings.Builder
	for _, line := range bootconfLines {
		prop, _, ok := strings.Cut(line, "=")
		if !ok {
			out.WriteString(line + "\n")
			continue
		}
		if extra, ok := extraByProp[prop]; ok {
			out.WriteString(extra + "\n")
		} else {
			out.WriteString(line + "\n")
		}
	}
	return []byte(out.String())
}
