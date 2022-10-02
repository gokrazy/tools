package config

import (
	"encoding/json"
	"log"
	"os"
	"path/filepath"

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

type Struct struct {
	Packages   []string // flag.Args()
	Hostname   string   // -hostname
	DeviceType string   `json:",omitempty"` // -device_type

	Update UpdateStruct `json:",omitempty"`

	// TODO: make per-package config overrideable here

	// Do not set these manually in config.json, these fields only exist so that
	// the entire old gokr-packer flag surface keeps working.
	InternalCompatibilityFlags InternalCompatibilityFlags
}

func InstancePath() string {
	return filepath.Join(instanceflag.InstanceDir(), instanceflag.Instance())
}

func ReadFromFile() (*Struct, error) {
	configJSON := filepath.Join(InstancePath(), "config.json")
	log.Printf("reading gokrazy config from %s", configJSON)
	b, err := os.ReadFile(configJSON)
	if err != nil {
		return nil, err
	}
	var cfg Struct
	if err := json.Unmarshal(b, &cfg); err != nil {
		return nil, err
	}
	return &cfg, nil
}
