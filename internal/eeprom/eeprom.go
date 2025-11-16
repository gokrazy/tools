// Package eeprom implements the Raspberry Pi EEPROM update file format.
package eeprom

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"strings"
)

// Other implementations:
//
// - https://github.com/raspberrypi/rpi-eeprom (Python)
// - https://github.com/info-beamer/rpi-eeprom-tools (Python)

const (
	MAGIC            = 0x55aaf00f
	MAGIC_MASK       = 0xfffff00f
	FILE_MAGIC       = 0x55aaf11f // id for modifiable files
	FILENAME_LEN     = 12
	FILENAME_PADDING = 4

	chunkHeaderLen = 4 + 4 // magic number + 32 bit offset
)

type Section struct {
	img      []byte
	Magic    uint32
	Offset   int
	Length   int
	Filename string
}

func FileSection(offset int, name string, contents []byte) *Section {
	if len(name) > FILENAME_LEN {
		panic(fmt.Sprintf("BUG: file name %s exceeds max FILENAME_LEN = %d", name, FILENAME_LEN))
	}

	img := bytes.Repeat([]byte{0}, offset+chunkHeaderLen)
	img = append(img, []byte(name)...)
	img = append(img, bytes.Repeat([]byte{0}, FILENAME_PADDING+FILENAME_LEN-len(name))...)
	img = append(img, contents...)
	return &Section{
		img:      img,
		Magic:    FILE_MAGIC,
		Offset:   offset,
		Length:   len(contents) + FILENAME_LEN + FILENAME_PADDING,
		Filename: name,
	}
}

func (s *Section) WithoutImg() Section {
	without := *s
	without.img = nil
	return without
}

func (s *Section) RawContent() []byte {
	offset := s.Offset + chunkHeaderLen
	length := s.Length
	return s.img[offset : offset+length]
}

func (s *Section) FileContent() []byte {
	offset := s.Offset + chunkHeaderLen
	length := s.Length
	if s.Magic == FILE_MAGIC {
		const fileSkip = FILENAME_LEN + FILENAME_PADDING
		offset += fileSkip
		length -= fileSkip
	}
	return s.img[offset : offset+length]
}

func Analyze(img []byte) ([]*Section, error) {
	// See https://github.com/raspberrypi/rpi-eeprom/blob/f38dbcb72341a3c3c3e66f1e10d58f8985cb0528/rpi-eeprom-config#L267

	if len(img) != 512*1024 &&
		len(img) != 2*1024*1024 {
		return nil, fmt.Errorf("unexpected EEPROM size: got %d, want 512KB or 2MB", len(img))
	}

	var sections []*Section
	for offset := 0; offset+chunkHeaderLen < len(img); {
		magic := binary.BigEndian.Uint32(img[offset : offset+4])
		length := binary.BigEndian.Uint32(img[offset+4 : offset+8])
		if magic == 0 || magic == 0xffffffff {
			break // end of file
		}
		if magic&MAGIC_MASK != MAGIC {
			return nil, fmt.Errorf("EEPROM is corrupted: %x & %x != %x", magic, MAGIC_MASK, MAGIC)
		}
		sect := Section{
			img:    img,
			Magic:  magic,
			Offset: offset,
			Length: int(length),
		}
		if magic == FILE_MAGIC {
			sect.Filename = string(img[offset+8 : offset+8+FILENAME_LEN])
			sect.Filename = strings.ReplaceAll(sect.Filename, "\x00", "")
		}
		sections = append(sections, &sect)
		offset += chunkHeaderLen + int(length)
		offset = (offset + 7) &^ 7
	}
	if len(sections) == 0 {
		return nil, fmt.Errorf("invalid EEPROM: no sections found")
	}
	if sections[len(sections)-1].Filename != "bootconf.txt" {
		// “by convention bootconf.txt is the last section”, from:
		// https://github.com/raspberrypi/rpi-eeprom/blob/f38dbcb72341a3c3c3e66f1e10d58f8985cb0528/rpi-eeprom-config#L373
		return nil, fmt.Errorf("invalid EEPROM: bootconf.txt not the last section")
	}
	return sections, nil
}

func Assemble(sections []*Section) []byte {
	output := bytes.Repeat([]byte{0xff}, len(sections[0].img))
	offset := 0
	for _, sect := range sections {
		binary.BigEndian.PutUint32(output[offset:], sect.Magic)
		binary.BigEndian.PutUint32(output[offset+4:], uint32(sect.Length))
		copy(output[offset+8:], sect.RawContent())
		offset += chunkHeaderLen + sect.Length
		offset = (offset + 7) &^ 7
	}
	return output
}
