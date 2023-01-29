package gok

import (
	"bytes"
	"testing"
)

func TestRandomMachineId(t *testing.T) {
	id, err := randomMachineId(bytes.NewReader(bytes.Repeat([]byte{0xfa}, 16)))
	if err != nil {
		t.Fatal(err)
	}
	printed := id.String()
	if got, want := len(printed), 32; got != want {
		t.Errorf("randomMachineId: result unexpectedly not 32 characters long: got %d, want %d", got, want)
	}
	if got, want := printed, "fafafafafafa4afabafafafafafafafa"; got != want {
		t.Errorf("randomMachineId: unexpected result: got %q, want %q", got, want)
	}
}
