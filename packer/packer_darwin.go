package packer

import (
	"fmt"
	"os"
)

func rereadPartitions(*os.File) error {
	return fmt.Errorf("gokrazy is currently missing code for re-reading partition tables on your operating system. Please see the README at https://github.com/gokrazy/tools for alternatives, and consider contributing code to fix this")
}
