package instanceflag

import (
	"os"

	"github.com/spf13/pflag"
)

var instance string

func RegisterPflags(fs *pflag.FlagSet) {
	def := os.Getenv("GOKRAZY_INSTANCE")
	if def == "" {
		def = "gokrazy"
	}
	fs.StringVarP(&instance,
		"instance",
		"i",
		def,
		`instance, identified by hostname`)
}

func Instance() string {
	return instance
}
