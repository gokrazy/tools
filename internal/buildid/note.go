// A stripped-down version of go/src/cmd/internal/buildid/note.go

// Copyright 2015 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package buildid

import (
	"bytes"
	"debug/elf"
	"io"
	"io/fs"
	"os"
)

var elfGoNote = []byte("Go\x00\x00")
var elfGNUNote = []byte("GNU\x00")

// The Go build ID is stored in a note described by an ELF PT_NOTE prog
// header. The caller has already opened filename, to get f, and read
// at least 4 kB out, in data.
func ReadELF(name string, f *os.File, data []byte) (buildid string, err error) {
	// Assume the note content is in the data, already read.
	// Rewrite the ELF header to set shoff and shnum to 0, so that we can pass
	// the data to elf.NewFile and it will decode the Prog list but not
	// try to read the section headers and the string table from disk.
	// That's a waste of I/O when all we care about is the Prog list
	// and the one ELF note.
	switch elf.Class(data[elf.EI_CLASS]) {
	case elf.ELFCLASS32:
		data[32], data[33], data[34], data[35] = 0, 0, 0, 0
		data[48] = 0
		data[49] = 0
	case elf.ELFCLASS64:
		data[40], data[41], data[42], data[43] = 0, 0, 0, 0
		data[44], data[45], data[46], data[47] = 0, 0, 0, 0
		data[60] = 0
		data[61] = 0
	}

	const elfGoBuildIDTag = 4
	const gnuBuildIDTag = 3

	ef, err := elf.NewFile(bytes.NewReader(data))
	if err != nil {
		return "", &fs.PathError{Path: name, Op: "parse", Err: err}
	}
	var gnu string
	for _, p := range ef.Progs {
		if p.Type != elf.PT_NOTE || p.Filesz < 16 {
			continue
		}

		var note []byte
		if p.Off+p.Filesz < uint64(len(data)) {
			note = data[p.Off : p.Off+p.Filesz]
		} else {
			// For some linkers, such as the Solaris linker,
			// the buildid may not be found in data (which
			// likely contains the first 16kB of the file)
			// or even the first few megabytes of the file
			// due to differences in note segment placement;
			// in that case, extract the note data manually.
			_, err = f.Seek(int64(p.Off), io.SeekStart)
			if err != nil {
				return "", err
			}

			note = make([]byte, p.Filesz)
			_, err = io.ReadFull(f, note)
			if err != nil {
				return "", err
			}
		}

		filesz := p.Filesz
		off := p.Off
		for filesz >= 16 {
			nameSize := ef.ByteOrder.Uint32(note)
			valSize := ef.ByteOrder.Uint32(note[4:])
			tag := ef.ByteOrder.Uint32(note[8:])
			nname := note[12:16]
			if nameSize == 4 && 16+valSize <= uint32(len(note)) && tag == elfGoBuildIDTag && bytes.Equal(nname, elfGoNote) {
				return string(note[16 : 16+valSize]), nil
			}

			if nameSize == 4 && 16+valSize <= uint32(len(note)) && tag == gnuBuildIDTag && bytes.Equal(nname, elfGNUNote) {
				gnu = string(note[16 : 16+valSize])
			}

			nameSize = (nameSize + 3) &^ 3
			valSize = (valSize + 3) &^ 3
			notesz := uint64(12 + nameSize + valSize)
			if filesz <= notesz {
				break
			}
			off += notesz
			align := p.Align
			if align != 0 {
				alignedOff := (off + align - 1) &^ (align - 1)
				notesz += alignedOff - off
				off = alignedOff
			}
			filesz -= notesz
			note = note[notesz:]
		}
	}

	// If we didn't find a Go note, use a GNU note if available.
	// This is what gccgo uses.
	if gnu != "" {
		return gnu, nil
	}

	// No note. Treat as successful but build ID empty.
	return "", nil
}
