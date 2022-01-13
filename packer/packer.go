// Package packer is a gokrazy-internal package which provides functionality
// shared between gokr-packer and rtr7-recovery-init.
package packer

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"hash/crc32"
	"hash/fnv"
	"io"
	"log"
	"os"
	"unicode/utf16"

	"golang.org/x/sys/unix"
)

// Pack represents one pack process.
type Pack struct {
	Partuuid       uint32
	UsePartuuid    bool
	UseGPTPartuuid bool
	UseGPT         bool
}

func NewPackForHost(hostname string) Pack {
	h := fnv.New32a()
	h.Write([]byte(hostname))
	return Pack{
		Partuuid:       h.Sum32(),
		UsePartuuid:    true,
		UseGPTPartuuid: true,
		UseGPT:         true,
	}
}

// ModifyCmdlineRoot() returns true if the -kernel_pkgs cmdline.txt file needs
// any modifications. This will be true on most gokrazy installations.
func (p *Pack) ModifyCmdlineRoot() bool {
	return p.UsePartuuid || p.UseGPTPartuuid
}

// GPTPARTUUID derives a GPT partition GUID for the specified partition.
//
// All gokrazy GPT partition GUIDs start with the same prefix and contain the
// hostname-derived hash + the partition number in the last block (“node
// identifier”).
func (p *Pack) GPTPARTUUID(partition uint16) string {
	const gokrazyGUIDPrefix = "60c24cc1-f3f9-427a-8199"
	return fmt.Sprintf("%s-%08x00%02x",
		gokrazyGUIDPrefix,
		p.Partuuid,
		partition)
}

func (p *Pack) Root() string {
	if p.UseGPTPartuuid {
		return fmt.Sprintf("PARTUUID=%s/PARTNROFF=1", p.GPTPARTUUID(1))
	}
	if p.UsePartuuid {
		return fmt.Sprintf("PARTUUID=%08x-02", p.Partuuid)
	}
	return "" // should only be called if ModifyCmdlineRoot()
}

func (p *Pack) PermUUID() string {
	if p.UseGPTPartuuid {
		return p.GPTPARTUUID(4)
	}
	if p.UsePartuuid {
		return fmt.Sprintf("%08x-04", p.Partuuid)
	}
	return "" // should only be called if ModifyCmdlineRoot()
}

var (
	active   = byte(0x80)
	inactive = byte(0x00)

	// invalidCHS results in using the sector values instead
	invalidCHS = [3]byte{0xFE, 0xFF, 0xFF}

	FAT      = byte(0xc)
	Linux    = byte(0x83)
	SquashFS = Linux // SquashFS does not have a dedicated type

	signature = uint16(0xAA55)
)

const MB = 1024 * 1024

func permSize(devsize uint64) uint32 {
	permStart := uint32(8192 + (1100 * MB / 512))
	permSize := uint32((devsize / 512) - 8192 - (1100 * MB / 512))
	// LBA -33 to LBA -1 need to remain unused for the secondary GPT header
	lastAddressable := uint32((devsize / 512) - 1) // 0-indexed
	if lastLBA := uint32(lastAddressable - 33); permStart+permSize >= lastLBA {
		permSize -= (permStart + permSize) - lastLBA
	}
	return permSize
}

// writePartitionTable writes a Hybrid MBR: it contains the GPT protective
// partition so that the Linux kernel recognizes the disk as GPT, but it also
// contains the FAT32 partition so that the Raspberry Pi bootloader still works.
func writePartitionTable(w io.Writer, devsize uint64) error {
	for _, v := range []interface{}{
		[446]byte{}, // boot code

		// Partition 1 must be present for the Raspberry Pi bootloader
		active,
		invalidCHS,
		FAT,
		invalidCHS,
		uint32(8192),           // start at 8192 sectors
		uint32(100 * MB / 512), // 100MB in size

		// Partition 2 is the protective GPT partition so that the Linux kernel
		// will recognize the disk as GPT.
		inactive,
		invalidCHS,
		byte(0xEE),
		invalidCHS,
		uint32(1),
		uint32(8191),

		[16]byte{}, // partition 3
		[16]byte{}, // partition 4

		signature,
	} {
		if err := binary.Write(w, binary.LittleEndian, v); err != nil {
			return err
		}
	}

	return nil
}

// writeMBRPartitionTable writes an MBR-only partition table. This is useful
// when the device requires blobs to be present in disk sectors otherwise occupied
// by GPT metadata. For example, Odroid HC2 clobbers sectors 1-2046 with binary blobs
// required for booting - these devices are incompatible with GPT. See
// https://wiki.odroid.com/odroid-xu4/software/partition_table#ubuntu_partition_table.
func writeMBRPartitionTable(w io.Writer, devsize uint64) error {
	for _, v := range []interface{}{
		[446]byte{}, // boot code

		// Partition 1 must be present for the Raspberry Pi bootloader
		active,
		invalidCHS,
		FAT,
		invalidCHS,
		uint32(8192),           // start at 8192 sectors
		uint32(100 * MB / 512), // 100MB in size

		// Partition 2 is squash partition 1.
		inactive,
		invalidCHS,
		Linux,
		invalidCHS,
		uint32(8192 + 100*MB/512),
		uint32(500 * MB / 512),

		// Partition 3 is squash partition 2.
		inactive,
		invalidCHS,
		Linux,
		invalidCHS,
		uint32(8192 + 600*MB/512),
		uint32(500 * MB / 512),

		// Partition 4 is the perm partition.
		inactive,
		invalidCHS,
		Linux,
		invalidCHS,
		uint32(8192 + 1100*MB/512),
		uint32(devsize/512 - 8192 - 1100*MB/512),

		signature,
	} {
		if err := binary.Write(w, binary.LittleEndian, v); err != nil {
			return err
		}
	}

	return nil
}

func mustParseGUID(guid string) [16]byte {
	// See Intel EFI specification, Appendix A: GUID and Time Formats
	// https://www.intel.de/content/dam/doc/product-specification/efi-v1-10-specification.pdf
	var (
		timeLow                 uint32
		timeMid                 uint16
		timeHighAndVersion      uint16
		clockSeqHighAndReserved uint8
		clockSeqLow             uint8
		node                    []byte
	)
	_, err := fmt.Sscanf(guid,
		"%08x-%04x-%04x-%02x%02x-%012x",
		&timeLow,
		&timeMid,
		&timeHighAndVersion,
		&clockSeqHighAndReserved,
		&clockSeqLow,
		&node)
	if err != nil {
		panic(err)
	}
	var result [16]byte
	binary.LittleEndian.PutUint32(result[0:4], timeLow)
	binary.LittleEndian.PutUint16(result[4:6], timeMid)
	binary.LittleEndian.PutUint16(result[6:8], timeHighAndVersion)
	result[8] = clockSeqHighAndReserved
	result[9] = clockSeqLow
	copy(result[10:], node)
	return result
}

func partitionName(name string) [72]byte {
	// adapted from https://github.com/diskfs/go-diskfs/blob/8a6b8b88d14a164cb914108d3cd829d9a67595a0/partition/gpt/partiton.go#L67

	// now the partition name - it is UTF16LE encoded, max 36 code units for 72 bytes
	r := make([]rune, 0, len(name))
	// first convert to runes
	for _, s := range name {
		r = append(r, rune(s))
	}
	if len(r) > 36 {
		panic(fmt.Sprintf("Cannot use %s as partition name, has %d Unicode code units, maximum size is 36", name, len(r)))
	}
	// next convert the runes to uint16
	nameb := utf16.Encode(r)
	// and then convert to little-endian bytes
	var result [72]byte
	for i, u := range nameb {
		pos := i * 2
		binary.LittleEndian.PutUint16(result[pos:pos+2], u)
	}
	return result
}

func (p *Pack) writeGPT(w io.Writer, devsize uint64, primary bool) error {
	const (
		partitionTypeEFISystemPartition      = "C12A7328-F81F-11D2-BA4B-00A0C93EC93B"
		partitionTypeLinuxFilesystemData     = "0FC63DAF-8483-4772-8E79-3D69D8477DE4"
		partitionTypeLinuxRootPartitionAMD64 = "4F68BCE3-E8CD-4DB1-96E7-FBCAF984B709"
		partitionTypeLinuxRootPartitionARM64 = "B921B045-1DF0-41C3-AF44-4C6F280D3FAE"
	)

	type partitionEntry struct {
		TypeGUID   [16]byte
		GUID       [16]byte
		FirstLBA   uint64
		LastLBA    uint64
		Attributes uint64
		Name       [72]byte
	}
	partition0First := uint64(8192)
	partition0Last := partition0First + (100 * MB / 512) - 1

	partition1First := partition0Last + 1
	partition1Last := partition1First + (500 * MB / 512) - 1

	partition2First := partition1Last + 1
	partition2Last := partition2First + (500 * MB / 512) - 1

	partition3First := partition2Last + 1
	partition3Last := partition3First + uint64(permSize(devsize)) - 1

	rootType := mustParseGUID(partitionTypeLinuxRootPartitionARM64)
	if os.Getenv("GOARCH") == "amd64" {
		rootType = mustParseGUID(partitionTypeLinuxRootPartitionAMD64)
	}
	partitionEntries := []partitionEntry{
		{
			TypeGUID:   mustParseGUID(partitionTypeEFISystemPartition),
			GUID:       mustParseGUID(p.GPTPARTUUID(1)),
			FirstLBA:   partition0First,
			LastLBA:    partition0Last,
			Attributes: 0,
			Name:       partitionName("Microsoft basic data"),
		},

		{
			TypeGUID:   rootType,
			GUID:       mustParseGUID(p.GPTPARTUUID(2)),
			FirstLBA:   partition1First,
			LastLBA:    partition1Last,
			Attributes: 0,
			Name:       partitionName("Linux filesystem"),
		},

		{
			TypeGUID:   mustParseGUID(partitionTypeLinuxFilesystemData),
			GUID:       mustParseGUID(p.GPTPARTUUID(3)),
			FirstLBA:   partition2First,
			LastLBA:    partition2Last,
			Attributes: 0,
			Name:       partitionName("Linux filesystem"),
		},

		{
			TypeGUID:   mustParseGUID(partitionTypeLinuxFilesystemData),
			GUID:       mustParseGUID(p.GPTPARTUUID(4)),
			FirstLBA:   partition3First,
			LastLBA:    partition3Last,
			Attributes: 0,
			Name:       partitionName("Linux filesystem"),
		},
	}
	var pbuf bytes.Buffer
	if err := binary.Write(&pbuf, binary.LittleEndian, partitionEntries); err != nil {
		return err
	}
	if _, err := pbuf.Write(bytes.Repeat([]byte{0}, (128-len(partitionEntries))*128)); err != nil {
		return err
	}
	entriesChecksum := crc32.ChecksumIEEE(pbuf.Bytes())

	lastAddressable := (devsize / 512) - 1 // 0-indexed
	currentLBA := uint64(1)
	backupLBA := lastAddressable
	entriesStart := uint64(2)
	if !primary {
		currentLBA = backupLBA
		entriesStart = backupLBA - 32
		backupLBA = 1
	}

	partitionHeader := struct {
		Signature      [8]byte
		Revision       uint32
		HeaderSize     uint32
		CRC32Header    uint32
		Reserved       uint32
		CurrentLBA     uint64
		BackupLBA      uint64
		FirstUsableLBA uint64
		LastUsableLBA  uint64
		DiskGUID       [16]byte
		EntriesStart   uint64
		EntriesCount   uint32
		EntriesSize    uint32
		CRC32Array     uint32
	}{
		Signature:      [8]byte{0x45, 0x46, 0x49, 0x20, 0x50, 0x41, 0x52, 0x54},
		Revision:       0x00010000, // Revision 1.0
		HeaderSize:     92,         // bytes
		CurrentLBA:     currentLBA,
		BackupLBA:      backupLBA,
		FirstUsableLBA: 34,
		LastUsableLBA:  lastAddressable - 32 - 1,
		DiskGUID:       mustParseGUID(p.GPTPARTUUID(0)),
		EntriesStart:   entriesStart,
		// From https://wiki.osdev.org/GPT:
		//
		// Note: it is somewhat vague what the Number of Partition Entries field
		// contain. For many applications that is the number of actually used
		// entries, while many partitioning tools (most notably fdisk and gdisk)
		// handles that as the number of maximum available entries, using full
		// zero Partition Type to mark empty entries. Unfortunately both
		// interpretation is suggested by the EFI spec, so this is unclear. One
		// thing is certain, there should be no more entries (empty or not) than
		// this field.
		EntriesCount: 128,
		EntriesSize:  128, // bytes
		CRC32Array:   entriesChecksum,
	}
	// Write to memory first to calculate CRC32
	var hbuf bytes.Buffer
	if err := binary.Write(&hbuf, binary.LittleEndian, partitionHeader); err != nil {
		return err
	}
	if got, want := hbuf.Len(), int(partitionHeader.HeaderSize); got != want {
		return fmt.Errorf("BUG: header size: got %d, want %d", got, want)
	}
	partitionHeader.CRC32Header = crc32.ChecksumIEEE(hbuf.Bytes())

	if !primary {
		// Write Partition entries (LBA 2-33):
		if _, err := io.Copy(w, &pbuf); err != nil {
			return err
		}
	}

	// Then write the Partition table header (LBA 1):
	if err := binary.Write(w, binary.LittleEndian, partitionHeader); err != nil {
		return err
	}

	for _, v := range []interface{}{
		[420]byte{}, // padding
	} {
		if err := binary.Write(w, binary.LittleEndian, v); err != nil {
			return err
		}
	}

	if primary {
		// Write Partition entries (LBA 2-33):
		if _, err := io.Copy(w, &pbuf); err != nil {
			return err
		}
	}

	return nil
}

func (p *Pack) Partition(o *os.File, devsize uint64) error {
	if !p.UseGPT {
		return writeMBRPartitionTable(o, devsize)
	}

	if err := writePartitionTable(o, devsize); err != nil {
		return err
	}

	// Write Primary GPT Header:
	if err := p.writeGPT(o, devsize, true /* primary */); err != nil {
		return err
	}

	// Write Secondary GPT Header:
	lastAddressable := (devsize / 512) - 1 // 0-indexed
	lbaMinus33 := lastAddressable - 32

	if _, err := o.Seek(int64(lbaMinus33*512), io.SeekStart); err != nil {
		return err
	}

	if err := p.writeGPT(o, devsize, false /* backup */); err != nil {
		return err
	}
	return nil
}

func (p *Pack) RereadPartitions(o *os.File) error {
	// Make Linux re-read the partition table. Sequence of system calls like in fdisk(8).
	unix.Sync()

	if err := rereadPartitions(uintptr(o.Fd())); err != nil {
		log.Printf("Re-reading partition table failed: %v. Remember to unplug and re-plug the SD card before creating a file system for persistent data, if desired.", err)
	}

	if err := o.Sync(); err != nil {
		return err
	}

	unix.Sync()
	return nil
}
