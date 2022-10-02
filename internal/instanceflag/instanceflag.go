package instanceflag

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/pflag"
)

var (
	instance    string
	instanceDir string
)

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

	homeDir, err := os.UserHomeDir()
	if err != nil {
		homeDir = fmt.Sprintf("os.UserHomeDir failed: %v", err)
	}
	fs.StringVar(&instanceDir,
		"instance_dir",
		filepath.Join(homeDir, "gokrazy"),
		`instance, identified by hostname`)

}

func Instance() string {
	return instance
}

func InstanceDir() string {
	return instanceDir
}
