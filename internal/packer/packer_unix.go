//go:build unix

package packer

import "syscall"

func setUmask() {
	// Ensure all build processes use umask 022. Programs like ntp which do
	// privilege separation need the o+x bit.
	syscall.Umask(0022)
}
