package packer

import (
	"os"
	"testing"
)

func TestSyncFileDevice(t *testing.T) {
	f, err := os.OpenFile("/dev/null", os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if err := syncFile(f); err != nil {
		t.Fatalf("syncFile(/dev/null) = %v, want nil", err)
	}
}
