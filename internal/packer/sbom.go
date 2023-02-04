package packer

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/gokrazy/internal/config"
	"github.com/gokrazy/internal/instanceflag"
	"github.com/gokrazy/tools/packer"
)

type FileHash struct {
	// Path is relative to the gokrazy instance directory (or absolute).
	Path string `json:"path"`

	// Hash is the SHA256 sum of the file.
	Hash string `json:"hash"`
}

type SBOM struct {
	// ConfigHash is the SHA256 sum of the gokrazy instance config (loaded
	// from config.json).
	ConfigHash FileHash `json:"config_hash"`

	// GoModHashes is list of FileHashes, sorted by path.
	//
	// It contains one entry for each go.mod file that was used to build a
	// gokrazy instance.
	GoModHashes []FileHash `json:"go_mod_hashes"`

	// ExtraFileHashes is list of FileHashes, sorted by path.
	//
	// It contains one entry for each file referenced via ExtraFilePaths:
	// https://gokrazy.org/userguide/instance-config/#packageextrafilepaths
	ExtraFileHashes []FileHash `json:"extra_file_hashes"`
}

type SBOMWithHash struct {
	SBOMHash string `json:"sbom_hash"`
	SBOM     SBOM   `json:"sbom"`
}

// GenerateSBOM generates a Software Bills Of Material (SBOM) for the
// local gokrazy instance.
func GenerateSBOM() ([]byte, SBOMWithHash, error) {
	cfg, err := config.ReadFromFile()
	if err != nil {
		if os.IsNotExist(err) {
			// best-effort compatibility for old setups
			cfg = config.NewStruct(instanceflag.Instance())
		} else {
			return nil, SBOMWithHash{}, err
		}
	}

	if err := os.Chdir(config.InstancePath()); err != nil {
		return nil, SBOMWithHash{}, err
	}

	formattedCfg, err := cfg.FormatForFile()
	if err != nil {
		return nil, SBOMWithHash{}, err
	}

	result := SBOM{
		ConfigHash: FileHash{
			Path: config.InstanceConfigPath(),
			Hash: fmt.Sprintf("%x", sha256.Sum256([]byte(formattedCfg))),
		},
	}

	extraFiles, err := FindExtraFiles(cfg)
	if err != nil {
		return nil, SBOMWithHash{}, err
	}

	packages := append(getGokrazySystemPackages(cfg), cfg.Packages...)

	for _, pkgAndVersion := range packages {
		pkg := pkgAndVersion
		if idx := strings.IndexByte(pkg, '@'); idx > -1 {
			pkg = pkg[:idx]
		}
		buildDir := packer.BuildDir(pkg)
		if _, err := os.Stat(buildDir); err != nil {
			wd, _ := os.Getwd()
			errStr := fmt.Sprintf("Error: build directory %q does not exist in %q\n", buildDir, wd)
			errStr += fmt.Sprintf("Try 'gok -i %s add %s' followed by an update.\n", instanceflag.Instance(), pkg)
			errStr += fmt.Sprintf("Afterwards, your 'gok sbom' command should work")
			return nil, SBOMWithHash{}, fmt.Errorf("%s: %w", errStr, err)
		}

		path := filepath.Join(buildDir, "go.mod")
		b, err := os.ReadFile(path)
		if err != nil {
			return nil, SBOMWithHash{}, err
		}
		result.GoModHashes = append(result.GoModHashes, FileHash{
			Path: path,
			Hash: fmt.Sprintf("%x", sha256.Sum256(b)),
		})

		files := append([]*FileInfo{}, extraFiles[pkg]...)
		for len(files) > 0 {
			fi := files[0]
			files = files[1:]
			files = append(files, fi.Dirents...)
			if fi.FromHost == "" {
				// Files that are not copied from the host are contained
				// fully in the config, which we already hashed.
				continue
			}

			path := fi.FromHost
			b, err := os.ReadFile(path)
			if err != nil {
				return nil, SBOMWithHash{}, err
			}
			result.ExtraFileHashes = append(result.ExtraFileHashes, FileHash{
				Path: path,
				Hash: fmt.Sprintf("%x", sha256.Sum256(b)),
			})
		}
	}

	sort.Slice(result.GoModHashes, func(i, j int) bool {
		a := result.GoModHashes[i]
		b := result.GoModHashes[j]
		return a.Path < b.Path
	})

	sort.Slice(result.ExtraFileHashes, func(i, j int) bool {
		a := result.ExtraFileHashes[i]
		b := result.ExtraFileHashes[j]
		return a.Path < b.Path
	})

	b, err := json.MarshalIndent(result, "", "    ")
	if err != nil {
		return nil, SBOMWithHash{}, err
	}
	b = append(b, '\n')

	sH := SBOMWithHash{
		SBOMHash: fmt.Sprintf("%x", sha256.Sum256(b)),
		SBOM:     result,
	}

	sM, err := json.MarshalIndent(sH, "", "    ")
	if err != nil {
		return nil, SBOMWithHash{}, err
	}
	sM = append(sM, '\n')

	return sM, sH, nil
}

func getGokrazySystemPackages(cfg *config.Struct) []string {
	pkgs := append([]string{}, cfg.GokrazyPackagesOrDefault()...)
	pkgs = append(pkgs, packer.InitDeps(cfg.InternalCompatibilityFlags.InitPkg)...)
	pkgs = append(pkgs, cfg.KernelPackageOrDefault())
	if fw := cfg.FirmwarePackageOrDefault(); fw != "" {
		pkgs = append(pkgs, fw)
	}
	if e := cfg.EEPROMPackageOrDefault(); e != "" {
		pkgs = append(pkgs, e)
	}
	return pkgs
}
