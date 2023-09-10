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
	"golang.org/x/mod/modfile"
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
func GenerateSBOM(cfg *config.Struct) ([]byte, SBOMWithHash, error) {
	wd, err := os.Getwd()
	if err != nil {
		return nil, SBOMWithHash{}, err
	}
	defer os.Chdir(wd)
	instancePath := config.InstancePath()
	if err := os.Chdir(instancePath); err != nil {
		if os.IsNotExist(err) {
			// best-effort compatibility for old setups
			instancePath = wd
		} else {
			return nil, SBOMWithHash{}, err
		}
	}

	formattedCfg, err := cfg.FormatForFile()
	if err != nil {
		return nil, SBOMWithHash{}, err
	}

	result := SBOM{
		ConfigHash: FileHash{
			Path: config.InstanceConfigPath(),
			Hash: fmt.Sprintf("%x", sha256.Sum256([]byte(string(formattedCfg)))),
		},
	}

	extraFiles, err := FindExtraFiles(cfg)
	if err != nil {
		return nil, SBOMWithHash{}, err
	}

	packages := append(getGokrazySystemPackages(cfg), cfg.Packages...)

	dirSeen := make(map[string]bool)

	for _, pkgAndVersion := range packages {
		pkg := pkgAndVersion
		if idx := strings.IndexByte(pkg, '@'); idx > -1 {
			pkg = pkg[:idx]
		}
		buildDir := packer.BuildDir(pkg)
		buildDir = filepath.Join(instancePath, buildDir)

		if err := os.Chdir(buildDir); err != nil {
			if os.IsNotExist(err) {
				wd, _ := os.Getwd()
				errStr := fmt.Sprintf("Error: build directory %q does not exist in %q\n", buildDir, wd)
				errStr += fmt.Sprintf("Try 'gok -i %s add %s' followed by an update.\n", instanceflag.Instance(), pkg)
				errStr += fmt.Sprintf("Afterwards, your 'gok sbom' command should work")
				return nil, SBOMWithHash{}, fmt.Errorf("%s: %w", errStr, err)
			} else {
				return nil, SBOMWithHash{}, err
			}
		}

		path, err := filepath.Abs("go.mod")
		if err != nil {
			return nil, SBOMWithHash{}, err
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return nil, SBOMWithHash{}, err
		}
		result.GoModHashes = append(result.GoModHashes, FileHash{
			Path: path,
			Hash: fmt.Sprintf("%x", sha256.Sum256(b)),
		})

		modf, err := modfile.Parse("go.mod", b, nil)
		if err != nil {
			return nil, SBOMWithHash{}, err
		}
		for _, r := range modf.Replace {
			if r.New.Version != "" {
				// replace directive that references a ModulePath
				continue
			}
			// replace directive that references a FilePath
			dir, err := filepath.Abs(r.New.Path)
			if err != nil {
				return nil, SBOMWithHash{}, err
			}
			// Especially when a go.mod template was used, the same replace
			// directive can be repeated many times across different packages,
			// hence we maintain a cache from dir to hash.
			if _, ok := dirSeen[dir]; !ok {
				h, err := hashDir(dir)
				if err != nil {
					return nil, SBOMWithHash{}, err
				}
				dirSeen[dir] = true
				result.GoModHashes = append(result.GoModHashes, FileHash{
					Path: dir,
					Hash: h,
				})
			}
		}

		files := append([]*FileInfo{}, extraFiles[pkg]...)
		if len(files) == 0 {
			continue
		}

		if err := os.Chdir(config.InstancePath()); err != nil {
			if os.IsNotExist(err) {
				// best-effort compatibility for old setups
			} else {
				return nil, SBOMWithHash{}, err
			}
		}

		for len(files) > 0 {
			fi := files[0]
			files = files[1:]
			files = append(files, fi.Dirents...)
			if fi.FromHost == "" {
				// Files that are not copied from the host are contained
				// fully in the config, which we already hashed.
				continue
			}

			path, err := filepath.Abs(fi.FromHost)
			if err != nil {
				return nil, SBOMWithHash{}, err
			}
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
