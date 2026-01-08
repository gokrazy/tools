package packer

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime/trace"
	"strings"
	"time"

	"github.com/gokrazy/internal/updateflag"
	"github.com/gokrazy/tools/packer"
)

func (pack *Pack) logicBuild(bindir string) error {
	log := pack.Env.Logger()

	cfg := pack.Cfg // for convenience
	args := cfg.Packages
	log.Printf("Building %d Go packages:", len(args))
	log.Printf("")
	for _, pkg := range args {
		log.Printf("  %s", pkg)
		for _, configFile := range packageConfigFiles[pkg] {
			log.Printf("    will %s",
				configFile.kind)
			if configFile.path != "" {
				log.Printf("      from %s",
					configFile.path)
			}
			if !configFile.lastModified.IsZero() {
				log.Printf("      last modified: %s (%s ago)",
					configFile.lastModified.Format(time.RFC3339),
					time.Since(configFile.lastModified).Round(1*time.Second))
			}
		}
		log.Printf("")
	}

	pkgs := append([]string{}, cfg.GokrazyPackagesOrDefault()...)
	pkgs = append(pkgs, cfg.Packages...)
	pkgs = append(pkgs, packer.InitDeps(cfg.InternalCompatibilityFlags.InitPkg)...)
	noBuildPkgs := []string{
		cfg.KernelPackageOrDefault(),
	}
	if fw := cfg.FirmwarePackageOrDefault(); fw != "" {
		noBuildPkgs = append(noBuildPkgs, fw)
	}
	if e := cfg.EEPROMPackageOrDefault(); e != "" {
		noBuildPkgs = append(noBuildPkgs, e)
	}
	setUmask()
	buildEnv := &packer.BuildEnv{
		BuildDir: packer.BuildDirOrMigrate,
	}
	var buildErr error
	trace.WithRegion(context.Background(), "build", func() {
		buildErr = buildEnv.Build(bindir, pkgs, pack.packageBuildFlags, pack.packageBuildTags, pack.packageBuildEnv, noBuildPkgs, pack.basenames)
	})
	if buildErr != nil {
		return buildErr
	}

	log.Printf("")

	var err error
	trace.WithRegion(context.Background(), "validate", func() {
		err = pack.validateTargetArchMatchesKernel()
	})
	if err != nil {
		return err
	}

	var (
		root      *FileInfo
		foundBins []foundBin
	)
	trace.WithRegion(context.Background(), "findbins", func() {
		root, foundBins, err = findBins(cfg, buildEnv, bindir, pack.basenames)
	})
	if err != nil {
		return err
	}
	pack.root = root

	packageConfigFiles = make(map[string][]packageConfigFile)

	var extraFiles map[string][]*FileInfo
	trace.WithRegion(context.Background(), "findextrafiles", func() {
		extraFiles, err = FindExtraFiles(cfg)
	})
	if err != nil {
		return err
	}
	for _, packageExtraFiles := range extraFiles {
		for _, ef := range packageExtraFiles {
			for _, de := range ef.Dirents {
				if de.Filename != "perm" {
					continue
				}
				return fmt.Errorf("invalid ExtraFilePaths or ExtraFileContents: cannot write extra files to user-controlled /perm partition")
			}
		}
	}

	if len(packageConfigFiles) > 0 {
		log.Printf("Including extra files for Go packages:")
		log.Printf("")
		for _, pkg := range args {
			if len(packageConfigFiles[pkg]) == 0 {
				continue
			}
			log.Printf("  %s", pkg)
			for _, configFile := range packageConfigFiles[pkg] {
				log.Printf("    will %s",
					configFile.kind)
				log.Printf("      from %s",
					configFile.path)
				log.Printf("      last modified: %s (%s ago)",
					configFile.lastModified.Format(time.RFC3339),
					time.Since(configFile.lastModified).Round(1*time.Second))
			}
			log.Printf("")
		}
	}

	if cfg.InternalCompatibilityFlags.InitPkg == "" {
		gokrazyInit := &gokrazyInit{
			root:             root,
			flagFileContents: pack.flagFileContents,
			envFileContents:  pack.envFileContents,
			buildTimestamp:   pack.buildTimestamp,
			dontStart:        pack.dontStart,
			waitForClock:     pack.waitForClock,
			waitFor:          pack.waitFor,
			basenames:        pack.basenames,
		}
		if cfg.InternalCompatibilityFlags.OverwriteInit != "" {
			return gokrazyInit.dump(cfg.InternalCompatibilityFlags.OverwriteInit)
		}

		var tmpdir string
		trace.WithRegion(context.Background(), "buildinit", func() {
			tmpdir, err = gokrazyInit.build()
		})
		if err != nil {
			return err
		}
		pack.initTmp = tmpdir

		initPath := filepath.Join(tmpdir, "init")

		fileIsELFOrFatal(initPath)

		gokrazy := root.mustFindDirent("gokrazy")
		gokrazy.Dirents = append(gokrazy.Dirents, &FileInfo{
			Filename: "init",
			FromHost: initPath,
		})
	}

	defaultPassword, updateHostname := updateflag.Value{
		Update: cfg.InternalCompatibilityFlags.Update,
	}.GetUpdateTarget(cfg.Hostname)
	update, err := cfg.Update.WithFallbackToHostSpecific(cfg.Hostname)
	if err != nil {
		return err
	}

	if update.HTTPPort == "" {
		update.HTTPPort = "80"
	}

	if update.HTTPSPort == "" {
		update.HTTPSPort = "443"
	}

	if update.Hostname == "" {
		update.Hostname = updateHostname
	}

	if update.HTTPPassword == "" && !update.NoPassword {
		pw, err := ensurePasswordFileExists(updateHostname, defaultPassword)
		if err != nil {
			return err
		}
		update.HTTPPassword = pw
	}

	pack.update = update

	for _, dir := range []string{"bin", "dev", "etc", "proc", "sys", "tmp", "perm", "lib", "run", "mnt"} {
		root.Dirents = append(root.Dirents, &FileInfo{
			Filename: dir,
		})
	}

	root.Dirents = append(root.Dirents, &FileInfo{
		Filename:    "var",
		SymlinkDest: "/perm/var",
	})

	mnt := root.mustFindDirent("mnt")
	for _, md := range cfg.MountDevices {
		if !strings.HasPrefix(md.Target, "/mnt/") {
			continue
		}
		rest := strings.TrimPrefix(md.Target, "/mnt/")
		rest = strings.TrimSuffix(rest, "/")
		if strings.Contains(rest, "/") {
			continue
		}
		mnt.Dirents = append(mnt.Dirents, &FileInfo{
			Filename: rest,
		})
	}

	// include lib/modules from kernelPackage dir, if present
	kernelDir, err := packer.PackageDir(cfg.KernelPackageOrDefault())
	if err != nil {
		return err
	}
	pack.kernelDir = kernelDir
	modulesDir := filepath.Join(kernelDir, "lib", "modules")
	if _, err := os.Stat(modulesDir); err == nil {
		log.Printf("Including loadable kernel modules from:")
		log.Printf("  %s", modulesDir)
		modules := &FileInfo{
			Filename: "modules",
		}
		trace.WithRegion(context.Background(), "kernelmod", func() {
			_, err = addToFileInfo(modules, modulesDir)
		})
		if err != nil {
			return err
		}
		lib := root.mustFindDirent("lib")
		lib.Dirents = append(lib.Dirents, modules)
	}

	etc := root.mustFindDirent("etc")
	tmpdir, err := os.MkdirTemp("", "gokrazy")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmpdir)
	hostLocaltime, err := hostLocaltime(tmpdir)
	if err != nil {
		return err
	}
	if hostLocaltime != "" {
		etc.Dirents = append(etc.Dirents, &FileInfo{
			Filename: "localtime",
			FromHost: hostLocaltime,
		})
	}
	etc.Dirents = append(etc.Dirents, &FileInfo{
		Filename:    "resolv.conf",
		SymlinkDest: "/tmp/resolv.conf",
	})
	etc.Dirents = append(etc.Dirents, &FileInfo{
		Filename: "hosts",
		FromLiteral: `127.0.0.1 localhost
::1 localhost
`,
	})
	etc.Dirents = append(etc.Dirents, &FileInfo{
		Filename:    "hostname",
		FromLiteral: cfg.Hostname,
	})

	ssl := &FileInfo{Filename: "ssl"}
	ssl.Dirents = append(ssl.Dirents, &FileInfo{
		Filename:    "ca-bundle.pem",
		FromLiteral: pack.systemCertsPEM,
	})

	pack.schema = "http"
	if update.CertPEM == "" || update.KeyPEM == "" {
		deployCertFile, deployKeyFile, err := getCertificate(cfg)
		if err != nil {
			return err
		}

		if deployCertFile != "" {
			b, err := os.ReadFile(deployCertFile)
			if err != nil {
				return err
			}
			update.CertPEM = strings.TrimSpace(string(b))

			b, err = os.ReadFile(deployKeyFile)
			if err != nil {
				return err
			}
			update.KeyPEM = strings.TrimSpace(string(b))
		}
	}
	if update.CertPEM != "" && update.KeyPEM != "" {
		// User requested TLS
		pack.schema = "https"

		ssl.Dirents = append(ssl.Dirents, &FileInfo{
			Filename:    "gokrazy-web.pem",
			FromLiteral: update.CertPEM,
		})
		ssl.Dirents = append(ssl.Dirents, &FileInfo{
			Filename:    "gokrazy-web.key.pem",
			FromLiteral: update.KeyPEM,
		})
	}

	etc.Dirents = append(etc.Dirents, ssl)

	if !update.NoPassword {
		etc.Dirents = append(etc.Dirents, &FileInfo{
			Filename:    "gokr-pw.txt",
			Mode:        0400,
			FromLiteral: update.HTTPPassword,
		})
	}

	etc.Dirents = append(etc.Dirents, &FileInfo{
		Filename:    "http-port.txt",
		FromLiteral: update.HTTPPort,
	})

	etc.Dirents = append(etc.Dirents, &FileInfo{
		Filename:    "https-port.txt",
		FromLiteral: update.HTTPSPort,
	})

	// GenerateSBOM() must be provided with a cfg
	// that hasn't been modified by gok at runtime,
	// as the SBOM should reflect what’s going into gokrazy,
	// not its internal implementation details
	// (i.e.  cfg.InternalCompatibilityFlags untouched).
	var sbom []byte
	var sbomWithHash SBOMWithHash
	trace.WithRegion(context.Background(), "sbom", func() {
		sbom, sbomWithHash, err = generateSBOM(pack.FileCfg, foundBins)
	})
	if err != nil {
		return err
	}
	pack.sbom = sbom
	pack.sbomWithHash = sbomWithHash

	etcGokrazy := &FileInfo{Filename: "gokrazy"}
	etcGokrazy.Dirents = append(etcGokrazy.Dirents, &FileInfo{
		Filename:    "sbom.json",
		FromLiteral: string(sbom),
	})
	mountdevices, err := json.Marshal(cfg.MountDevices)
	if err != nil {
		return err
	}
	etcGokrazy.Dirents = append(etcGokrazy.Dirents, &FileInfo{
		Filename:    "mountdevices.json",
		FromLiteral: string(mountdevices),
	})
	etc.Dirents = append(etc.Dirents, etcGokrazy)

	empty := &FileInfo{Filename: ""}
	if paths := getDuplication(root, empty); len(paths) > 0 {
		return fmt.Errorf("root file system contains duplicate files: your config contains multiple packages that install %s", paths)
	}

	for pkg1, fs := range extraFiles {
		for _, fs1 := range fs {
			// check against root fs
			if paths := getDuplication(root, fs1); len(paths) > 0 {
				return fmt.Errorf("extra files of package %s collides with root file system: %v", pkg1, paths)
			}

			// check against other packages
			for pkg2, fs := range extraFiles {
				for _, fs2 := range fs {
					if pkg1 == pkg2 {
						continue
					}

					if paths := getDuplication(fs1, fs2); len(paths) > 0 {
						return fmt.Errorf("extra files of package %s collides with package %s: %v", pkg1, pkg2, paths)
					}
				}
			}

			// add extra files to rootfs
			if err := root.combine(fs1); err != nil {
				return fmt.Errorf("failed to add extra files from package %s: %v", pkg1, err)
			}
		}
	}

	return nil
}
