//go:build linux

package cap

import (
	"sort"
	"syscall"
	"unsafe"
)

// cInit performs the lazy identification of the capability vintage of
// the running system.
func cInit() {
	h := &header{
		magic: kv3,
	}
	_, _, _ = syscall.RawSyscall(syscall.SYS_CAPGET, uintptr(unsafe.Pointer(h)), uintptr(0), 0)

	magic = h.magic
	switch magic {
	case kv1:
		words = 1
	case kv2, kv3:
		words = 2
	default:
		// Fall back to a known good version.
		magic = kv3
		words = 2
	}
	// Use the bounding set to evaluate which capabilities exist.
	maxValues = uint(sort.Search(32*words, func(n int) bool {
		_, err := GetBound(Value(n))
		return err != nil
	}))
	if maxValues == 0 {
		// Fall back to using the largest value defined at build time.
		maxValues = NamedCount
	}
}
func GetBound(val Value) (bool, error) {
	r, _, err := syscall.RawSyscall(syscall.SYS_PRCTL, prCapBSetRead, uintptr(val), 0)
	if err != 0 {
		return false, err
	}

	return int(r) > 0, nil
}
