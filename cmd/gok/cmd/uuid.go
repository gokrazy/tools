package cmd

import (
	"fmt"
	"io"
)

// id128 is a 16 byte identifier
type id128 []byte

func newId128() id128 {
	return make(id128, 16)
}

func (i id128) String() string {
	return fmt.Sprintf("%x", []byte(i))
}

func randomMachineId(r io.Reader) (id128, error) {
	id := newId128()
	if _, err := io.ReadFull(r, id); err != nil {
		return nil, fmt.Errorf("reading random bytes: %v", err)
	}

	// Turn the id into a valid v4 UUID like systemd does:
	// https://github.com/systemd/systemd/blob/fc2a0bc05e0429e468c7eaad52998292105fe7fb/src/libsystemd/sd-id128/id128-util.c#L162

	// Set UUID version to 4, truly random generation
	id[6] = (id[6] & 0x0F) | 0x40

	// Set UUID variant to DCE
	id[8] = (id[8] & 0x3F) | 0x80

	return id, nil
}
