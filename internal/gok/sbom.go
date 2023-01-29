package gok

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/gokrazy/internal/config"
	"github.com/gokrazy/internal/instanceflag"
	"github.com/gokrazy/internal/updateflag"
	internalpacker "github.com/gokrazy/tools/internal/packer"
	"github.com/gokrazy/tools/packer"
	"github.com/spf13/cobra"
)

// sbomCmd is gok sbom.
var sbomCmd = &cobra.Command{
	GroupID: "deploy",
	Use:     "sbom",
	Short:   "Print the Software Bill Of Materials of a gokrazy instance",
	Long: `gok sbom generates an SBOM of what gok overwrite or gok update would build

Examples:
  # print the hash and SBOM contents in JSON format
  % gok -i scanner sbom

  # show only the hash of the SBOM
  % gok -i scanner sbom --format hash

`,
	RunE: func(cmd *cobra.Command, args []string) error {
		return sbomImpl.run(cmd.Context(), args, cmd.OutOrStdout(), cmd.OutOrStderr())
	},
}

type sbomConfig struct {
	format string
}

var sbomImpl sbomConfig

func init() {
	sbomCmd.Flags().StringVarP(&sbomImpl.format, "format", "", "json", "output format. one of json or hash")
	instanceflag.RegisterPflags(sbomCmd.Flags())
}

func (r *sbomConfig) run(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	cfg, err := config.ReadFromFile()
	if err != nil {
		if os.IsNotExist(err) {
			// best-effort compatibility for old setups
			cfg = config.NewStruct(instanceflag.Instance())
		} else {
			return err
		}
	}

	if err := os.Chdir(config.InstancePath()); err != nil {
		return err
	}

	updateflag.SetUpdate("yes")

	type fileHash struct {
		// Path is relative to the gokrazy instance directory (or absolute).
		Path string `json:"path"`

		// Hash is the SHA256 sum of the file.
		Hash string `json:"hash"`
	}

	type sbom struct {
		// ConfigHash is the SHA256 sum of the gokrazy instance config (loaded
		// from config.json).
		ConfigHash fileHash `json:"config_hash"`

		// GoModHashes is list of fileHashes, sorted by path.
		//
		// It contains one entry for each go.mod file that was used to build a
		// gokrazy instance.
		GoModHashes []fileHash `json:"go_mod_hashes"`

		// ExtraFileHashes is list of fileHashes, sorted by path.
		//
		// It contains one entry for each file referenced via ExtraFilePaths:
		// https://gokrazy.org/userguide/instance-config/#packageextrafilepaths
		ExtraFileHashes []fileHash `json:"extra_file_hashes"`
	}

	formattedCfg, err := cfg.FormatForFile()
	if err != nil {
		return err
	}

	result := sbom{
		ConfigHash: fileHash{
			Path: config.InstanceConfigPath(),
			Hash: fmt.Sprintf("%x", sha256.Sum256([]byte(formattedCfg))),
		},
	}

	extraFiles, err := internalpacker.FindExtraFiles(cfg)
	if err != nil {
		return err
	}

	packages := append(getGokrazySystemPackages(cfg), cfg.Packages...)

	for _, pkgAndVersion := range packages {
		pkg := pkgAndVersion
		if idx := strings.IndexByte(pkg, '@'); idx > -1 {
			pkg = pkg[:idx]
		}
		buildDir := packer.BuildDir(pkg)
		_, err := os.Stat(buildDir)
		if os.IsNotExist(err) {
			// Common case, handle with a good error message
			wd, _ := os.Getwd()
			os.Stderr.WriteString("\n")
			log.Printf("Error: build directory %q does not exist in %q", buildDir, wd)
			log.Printf("Try 'gok -i %s add %s' followed by an update.", instanceflag.Instance(), pkg)
			log.Printf("Afterwards, your 'gok sbom' command should work")
			return nil
		}
		if err != nil {
			return err
		}

		path := filepath.Join(buildDir, "go.mod")
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		result.GoModHashes = append(result.GoModHashes, fileHash{
			Path: path,
			Hash: fmt.Sprintf("%x", sha256.Sum256(b)),
		})

		files := append([]*internalpacker.FileInfo{}, extraFiles[pkg]...)
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
				return err
			}
			result.ExtraFileHashes = append(result.ExtraFileHashes, fileHash{
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
		return err
	}
	b = append(b, '\n')

	sbomWithHash := struct {
		SBOMHash string `json:"sbom_hash"`
		SBOM     sbom   `json:"sbom"`
	}{
		SBOMHash: fmt.Sprintf("%x", sha256.Sum256(b)),
		SBOM:     result,
	}
	b, err = json.MarshalIndent(sbomWithHash, "", "    ")
	if err != nil {
		return err
	}
	b = append(b, '\n')
	if r.format == "json" {
		stdout.Write(b)
	} else if r.format == "hash" {
		fmt.Fprintf(stdout, "%s\n", sbomWithHash.SBOMHash)
	} else {
		return fmt.Errorf("unknown format: expected one of json or hash")
	}

	return nil
}
