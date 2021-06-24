package main

import (
	"bytes"
	"testing"
)

func TestMustParseGUID(t *testing.T) {
	const guid = "EBD0A0A2-B9E5-4433-87C0-68B6B72699C7"
	got := mustParseGUID(guid)
	want := [16]byte{
		162, 160, 208, 235, 229, 185, 51, 68, 135, 192, 104, 182, 183, 38, 153, 199,
	}
	if !bytes.Equal(got[:], want[:]) {
		t.Fatalf("mustParseGUID(%s) = %x, want %x", guid, got, want)
	}
}
