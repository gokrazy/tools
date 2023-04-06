package packer

import (
	"os"
	"testing"
)

func TestKernelGoarch(t *testing.T) {
	for _, arch := range []string{"arm", "arm64", "amd64"} {
		t.Run(arch, func(t *testing.T) {
			k, err := os.ReadFile("testdata/kernel." + arch)
			if err != nil {
				t.Fatal(err)
			}
			got := kernelGoarch(k)
			if got != arch {
				t.Errorf("got %q; want %q", got, arch)
			}
		})
	}
}
