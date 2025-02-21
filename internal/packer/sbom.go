package packer

import (
	"crypto/sha256"
	"debug/buildinfo"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"sync"

	"github.com/gokrazy/internal/config"
	"github.com/gokrazy/tools/internal/buildid"
	"github.com/gokrazy/tools/packer"
	"golang.org/x/sync/errgroup"
)

type FileHash struct {
	// Path is relative to the gokrazy instance directory (or absolute).
	Path string `json:"path"`

	// Hash is the SHA256 sum of the file.
	Hash string `json:"hash"`
}

// GoPackage identifies a built Go binary via its BuildInfo (human readable) and
// BuildID. When installing a non-local package, the BuildInfo will be
// sufficient to reproduce exactly this binary. For local packages, any change
// to the source will just show up as (dirty) in BuildInfo, so we record the
// BuildID in addition, which will change whenever the source changes.
type GoPackage struct {
	// Path is an absolute path on the gokrazy instance, e.g. /gokrazy/init
	Path string `json:"path"`

	// BuildID contains the Go (or GNU) build ID.
	BuildID string

	// BuildInfo contains the String representation of debug.BuildInfo
	BuildInfo string
}

type SBOM struct {
	// ConfigHash is the SHA256 sum of the gokrazy instance config (loaded
	// from config.json).
	ConfigHash FileHash `json:"config_hash"`

	// ExtraFileHashes is list of FileHashes, sorted by path.
	//
	// It contains one entry for each file referenced via ExtraFilePaths:
	// https://gokrazy.org/userguide/instance-config/#packageextrafilepaths
	ExtraFileHashes []FileHash `json:"extra_file_hashes"`

	// GoPackages contains one entry per installed package of the gokrazy
	// instance.
	GoPackages []GoPackage `json:"go_packages"`
}

type SBOMWithHash struct {
	SBOMHash string `json:"sbom_hash"`
	SBOM     SBOM   `json:"sbom"`
}

func readBuildID(f *os.File) (string, error) {
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return "", err
	}
	const readSize = 32 * 1024
	data := make([]byte, readSize)
	_, err := io.ReadFull(f, data)
	if err == io.ErrUnexpectedEOF {
		err = nil
	}
	if err != nil {
		return "", err
	}
	return buildid.ReadELF(f.Name(), f, data)
}

// generateSBOM generates a Software Bills Of Material (SBOM) for the
// local gokrazy instance.
// It must be provided with a cfg that hasn't been modified by gok at runtime,
// as the SBOM should reflect what’s going into gokrazy,
// not its internal implementation details
// (i.e.  cfg.InternalCompatibilityFlags untouched).
func generateSBOM(cfg *config.Struct, foundBins []foundBin) ([]byte, SBOMWithHash, error) {
	instancePath, err := os.Getwd()
	if err != nil {
		return nil, SBOMWithHash{}, err
	}
	defer os.Chdir(instancePath)

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

	var (
		eg           errgroup.Group
		goPackagesMu sync.Mutex
	)
	for _, bin := range foundBins {
		eg.Go(func() error {
			f, err := os.Open(bin.hostPath)
			if err != nil {
				return err
			}
			info, err := buildinfo.Read(f)
			if err != nil {
				return err
			}
			id, err := readBuildID(f)
			if err != nil {
				return err
			}

			goPackagesMu.Lock()
			defer goPackagesMu.Unlock()
			result.GoPackages = append(result.GoPackages, GoPackage{
				Path:      bin.gokrazyPath,
				BuildID:   id,
				BuildInfo: info.String(),
			})
			return nil
		})
	}
	if err := eg.Wait(); err != nil {
		return nil, SBOMWithHash{}, err
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

		files := append([]*FileInfo{}, extraFiles[pkg]...)
		if len(files) == 0 {
			continue
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

			b, err := os.ReadFile(fi.FromHost /* already absolute */)
			if err != nil {
				return nil, SBOMWithHash{}, err
			}
			result.ExtraFileHashes = append(result.ExtraFileHashes, FileHash{
				Path: fi.FromHost,
				Hash: fmt.Sprintf("%x", sha256.Sum256(b)),
			})
		}
	}

	sort.Slice(result.GoPackages, func(i, j int) bool {
		pi := result.GoPackages[i]
		pj := result.GoPackages[j]
		return pi.Path < pj.Path
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
