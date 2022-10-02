package config

import (
	"encoding/json"
	"log"
	"os"
	"path/filepath"

	"github.com/gokrazy/tools/internal/instanceflag"
)

type Struct struct {
	Packages []string
}

func ReadFromFile() (*Struct, error) {
	configJSON := filepath.Join(instanceflag.InstanceDir(), instanceflag.Instance(), "config.json")
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
