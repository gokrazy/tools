//go:build !unix

package packer

import (
	"fmt"
	"os"
	"runtime"
)

func deviceSize(fd uintptr) (uint64, error) {
	return 0, fmt.Errorf("gokrazy is currently missing code for getting device sizes on your operating system. Please see the README at https://github.com/gokrazy/tools for alternatives, and consider contributing code to fix this")
}

func rereadPartitions(fd uintptr) error {
	return fmt.Errorf("gokrazy is currently missing code for re-reading partition tables on your operating system. Please see the README at https://github.com/gokrazy/tools for alternatives, and consider contributing code to fix this")
}

func (p *Pack) SudoPartition(path string) (*os.File, error) {
	return nil, fmt.Errorf("gokrazy is currently missing code for elevating privileges on %s", runtime.GOOS)
}
