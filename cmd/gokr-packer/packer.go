// gokr-packer compiles and installs the specified Go packages as well
// as the gokrazy Go packages and packs them into an SD card image for
// devices supported by gokrazy (see https://gokrazy.org/platforms/).
package main

import (
	"github.com/gokrazy/tools/internal/oldpacker"

	// Imported so that the go tool will download the repositories
	_ "github.com/gokrazy/gokrazy/empty"
)

func main() {
	oldpacker.Main()
}
