// +build !linux,!darwin

package main

import "fmt"

func deviceSize(fd uintptr) (uint64, error) {
	return 0, fmt.Errorf("gokrazy is currently missing code for getting device sizes on your operating system. Please see the README at https://github.com/gokrazy/tools for alternatives, and consider contributing code to fix this")
}
