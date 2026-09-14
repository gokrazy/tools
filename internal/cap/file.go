package cap

import (
	"bytes"
	"encoding/binary"
	"errors"
	"io"
	"syscall"
)

// uapi/linux/xattr.h defined.
var (
	xattrNameCaps, _ = syscall.BytePtrFromString("security.capability")
)

// uapi/linux/capability.h defined.
const (
	vfsCapRevisionMask   = uint32(0xff000000)
	vfsCapFlagsMask      = ^vfsCapRevisionMask
	vfsCapFlagsEffective = uint32(1)

	vfsCapRevision1 = uint32(0x01000000)
	vfsCapRevision2 = uint32(0x02000000)
	vfsCapRevision3 = uint32(0x03000000)
)

// Data types stored in little-endian order.

type vfsCaps1 struct {
	MagicEtc uint32
	Data     [1]struct {
		Permitted, Inheritable uint32
	}
}

type vfsCaps2 struct {
	MagicEtc uint32
	Data     [2]struct {
		Permitted, Inheritable uint32
	}
}

type vfsCaps3 struct {
	MagicEtc uint32
	Data     [2]struct {
		Permitted, Inheritable uint32
	}
	RootID uint32
}

// ErrBadSize indicates the loaded file capability has
// an invalid number of bytes in it.
var ErrBadSize = errors.New("filecap bad size")

// ErrBadMagic indicates that the kernel preferred magic number for
// capability Set values is not supported by this package. This
// generally implies you are using an exceptionally old
// "../libcap/cap" package. An upgrade is needed, or failing that see
// [the Fully Capable site] for the way to report or review a bug.
//
// [the Fully Capable site]: https://sites.google.com/site/fullycapable/
var ErrBadMagic = errors.New("unsupported magic")

// ErrBadPath indicates a failed attempt to set a file capability on
// an irregular (non-executable) file.
var ErrBadPath = errors.New("file is not a regular executable")

// ErrOutOfRange indicates an erroneous value for MinExtFlagSize.
var ErrOutOfRange = errors.New("flag length invalid for export")

// DigestFileCap unpacks a file capability and returns it in a *Set
// form.
func DigestFileCap(d []byte) (*Set, error) {
	var (
		err  error
		raw1 vfsCaps1
		raw2 vfsCaps2
		raw3 vfsCaps3
	)
	sz := len(d)
	if sz < binary.Size(raw1) || sz > binary.Size(raw3) {
		return nil, ErrBadSize
	}
	b := bytes.NewReader(d)
	var magicEtc uint32
	if err = binary.Read(b, binary.LittleEndian, &magicEtc); err != nil {
		return nil, err
	}

	c := NewSet()
	b.Seek(0, io.SeekStart)
	switch magicEtc & vfsCapRevisionMask {
	case vfsCapRevision1:
		if err = binary.Read(b, binary.LittleEndian, &raw1); err != nil {
			return nil, err
		}
		data := raw1.Data[0]
		c.flat[0][Permitted] = data.Permitted
		c.flat[0][Inheritable] = data.Inheritable
		if raw1.MagicEtc&vfsCapFlagsMask == vfsCapFlagsEffective {
			c.flat[0][Effective] = data.Inheritable | data.Permitted
		}
	case vfsCapRevision2:
		if err = binary.Read(b, binary.LittleEndian, &raw2); err != nil {
			return nil, err
		}
		for i, data := range raw2.Data {
			c.flat[i][Permitted] = data.Permitted
			c.flat[i][Inheritable] = data.Inheritable
			if raw2.MagicEtc&vfsCapFlagsMask == vfsCapFlagsEffective {
				c.flat[i][Effective] = data.Inheritable | data.Permitted
			}
		}
	case vfsCapRevision3:
		if err = binary.Read(b, binary.LittleEndian, &raw3); err != nil {
			return nil, err
		}
		for i, data := range raw3.Data {
			c.flat[i][Permitted] = data.Permitted
			c.flat[i][Inheritable] = data.Inheritable
			if raw3.MagicEtc&vfsCapFlagsMask == vfsCapFlagsEffective {
				c.flat[i][Effective] = data.Inheritable | data.Permitted
			}
		}
		c.nsRoot = int(raw3.RootID)
	default:
		return nil, ErrBadMagic
	}
	return c, nil
}

// PackFileCap transforms a system capability into a VFS form. Because
// of the way Linux stores capabilities in the file extended
// attributes, the process is a little lossy with respect to effective
// bits.
func (c *Set) PackFileCap() ([]byte, error) {
	c.mu.RLock()
	defer c.mu.RUnlock()

	var magic uint32
	switch words {
	case 1:
		if c.nsRoot != 0 {
			return nil, ErrBadSet // nsRoot not supported for single DWORD caps.
		}
		magic = vfsCapRevision1
	case 2:
		if c.nsRoot == 0 {
			magic = vfsCapRevision2
			break
		}
		magic = vfsCapRevision3
	}
	if magic == 0 {
		return nil, ErrBadSize
	}
	eff := uint32(0)
	for _, f := range c.flat {
		eff |= (f[Permitted] | f[Inheritable]) & f[Effective]
	}
	if eff != 0 {
		magic |= vfsCapFlagsEffective
	}
	b := new(bytes.Buffer)
	binary.Write(b, binary.LittleEndian, magic)
	for _, f := range c.flat {
		binary.Write(b, binary.LittleEndian, f[Permitted])
		binary.Write(b, binary.LittleEndian, f[Inheritable])
	}
	if c.nsRoot != 0 {
		binary.Write(b, binary.LittleEndian, uint32(c.nsRoot))
	}
	return b.Bytes(), nil
}
