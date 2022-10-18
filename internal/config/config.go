package config

import (
	"encoding/json"
	"io"
	"log"
	"os"
	"path/filepath"
	"time"

	"github.com/gokrazy/tools/internal/instanceflag"
)

type InternalCompatibilityFlags struct {
	GokrazyPackages    []string `json:",omitempty"` // -gokrazy_pkgs
	Overwrite          string   `json:",omitempty"` // -overwrite
	OverwriteBoot      string   `json:",omitempty"` // -overwrite_boot
	OverwriteMBR       string   `json:",omitempty"` // -overwrite_mbr
	OverwriteRoot      string   `json:",omitempty"` // -overwrite_root
	TargetStorageBytes int      `json:",omitempty"` // -target_storage_bytes
	InitPkg            string   `json:",omitempty"` // -init_pkg
	OverwriteInit      string   `json:",omitempty"` // -overwrite_init
	Testboot           bool     `json:",omitempty"` // -testboot
	Sudo               string   `json:",omitempty"` // -sudo
	Update             string   `json:",omitempty"` // -update
	Insecure           bool     `json:",omitempty"` // -insecure
	UseTLS             string   `json:",omitempty"` // -tls
	Env                []string `json:",omitempty"` // environment variables starting with GO
}

type UpdateStruct struct {
	Hostname  string `json:",omitempty"` // overrides Struct.Hostname
	HttpPort  string `json:",omitempty"` // -http_port
	HttpsPort string `json:",omitempty"` // -https_port

	// TODO: make http-password.txt, http-port.txt, cert.pem, key.pem overrideable here
}

type PackageConfig struct {
	// GoBuildFlags will be passed to “go build” as extra arguments.
	//
	// To pass build tags, do not use -tags=mycustomtag; instead set the
	// GoBuildTags field to not overwrite the gokrazy default build tags.
	GoBuildFlags []string `json:",omitempty"`

	// GoBuildTags will be added to the list of gokrazy default build tags.
	GoBuildTags []string `json:",omitempty"`

	// Environment contains key=value pairs, like in Go’s os.Environ().
	Environment []string `json:",omitempty"`

	// CommandLineFlags will be set when starting the program.
	CommandLineFlags []string `json:",omitempty"`

	// DontStart makes the gokrazy init not start this program
	// automatically. Users can still start it manually via the web interface,
	// or interactively via breakglass.
	DontStart bool `json:",omitempty"`

	// WaitForClock makes the gokrazy init wait for clock synchronization before
	// starting the program. This is useful when modifying the program source to
	// call gokrazy.WaitForClock() is inconvenient.
	WaitForClock bool `json:",omitempty"`

	// ExtraFilePaths maps from root file system destination path to a relative
	// or absolute path on the host on which the packer is running.
	//
	// Lookup order:
	// 1. <path>_<target_goarch>.tar
	// 2. <path>.tar
	// 3. <path> (directory)
	ExtraFilePaths map[string]string `json:",omitempty"`

	// ExtraFileContents maps from root file system destination path to the
	// plain text contents of the file.
	ExtraFileContents map[string]string `json:",omitempty"`
}

type Struct struct {
	Packages   []string // flag.Args()
	Hostname   string   // -hostname
	DeviceType string   `json:",omitempty"` // -device_type

	Update UpdateStruct `json:",omitempty"`

	// If PackageConfig is specified, all package config is taken from the
	// config struct, no longer from the file system, except for extrafiles/.
	PackageConfig map[string]PackageConfig `json:",omitempty"`

	// Do not set these manually in config.json, these fields only exist so that
	// the entire old gokr-packer flag surface keeps working.
	InternalCompatibilityFlags InternalCompatibilityFlags

	Meta struct {
		Instance     string
		Path         string
		LastModified time.Time
	} `json:"-"` // omit from JSON
}

func InstancePath() string {
	return filepath.Join(instanceflag.InstanceDir(), instanceflag.Instance())
}

func ReadFromFile() (*Struct, error) {
	configJSON := filepath.Join(InstancePath(), "config.json")
	log.Printf("reading gokrazy config from %s", configJSON)
	f, err := os.Open(configJSON)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return nil, err
	}
	b, err := io.ReadAll(f)
	if err != nil {
		return nil, err
	}
	var cfg Struct
	if err := json.Unmarshal(b, &cfg); err != nil {
		return nil, err
	}
	cfg.Meta.Instance = instanceflag.Instance()
	cfg.Meta.Path = configJSON
	cfg.Meta.LastModified = st.ModTime()
	return &cfg, nil
}
