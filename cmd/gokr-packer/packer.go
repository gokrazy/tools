// gokr-packer compiles and installs the specified Go packages as well
// as the gokrazy Go packages and packs them into an SD card image for
// devices supported by gokrazy (see https://gokrazy.org/platforms/).
package main

import "github.com/gokrazy/tools/internal/oldpacker"

func main() {
	oldpacker.Main()
}
