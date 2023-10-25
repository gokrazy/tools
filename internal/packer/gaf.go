package packer

import (
	"archive/zip"
	"io"
	"os"
	"path/filepath"
)

// overwriteGaf writes a gaf (gokrazy archive format) file
// by packing build artifacts and
// storing them into a newly created, uncompressed zip.
func (p *Pack) overwriteGaf(root *FileInfo) error {
	dir, err := os.MkdirTemp("", "gokrazy")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)

	tmpMBR, err := os.Create(filepath.Join(dir, "mbr.img"))
	if err != nil {
		return err
	}
	defer os.Remove(tmpMBR.Name())

	tmpBoot, err := os.Create(filepath.Join(dir, "boot.img"))
	if err != nil {
		return err
	}
	defer os.Remove(tmpBoot.Name())

	tmpRoot, err := os.Create(filepath.Join(dir, "root.img"))
	if err != nil {
		return err
	}
	defer os.Remove(tmpRoot.Name())

	tmpSBOM, err := os.Create(filepath.Join(dir, "sbom.json"))
	if err != nil {
		return err
	}
	defer os.Remove(tmpSBOM.Name())

	if err := p.writeBoot(tmpBoot, tmpMBR.Name()); err != nil {
		return err
	}

	if err := writeRoot(tmpRoot, root); err != nil {
		return err
	}

	// GenerateSBOM() must be provided with a cfg
	// that hasn't been modified by gok at runtime,
	// as the SBOM should reflect what’s going into gokrazy,
	// not its internal implementation details
	// (i.e.  cfg.InternalCompatibilityFlags untouched).
	sbomMarshaled, _, err := GenerateSBOM(p.FileCfg)
	if err != nil {
		return err
	}

	if _, err := tmpSBOM.Write(sbomMarshaled); err != nil {
		return err
	}

	tmpMBR.Close()
	tmpBoot.Close()
	tmpRoot.Close()
	tmpSBOM.Close()

	if err := writeGafArchive(dir, p.Output.Path); err != nil {
		return err
	}

	return nil
}

// writeGafArchive archives build artifacts into
// a gaf (gokrazy archive format) file
// by reading artifacts from a source directory
// and storing them into a newly created, uncompressed zip.
func writeGafArchive(sourceDir, targetFile string) error {
	f, err := os.Create(targetFile)
	if err != nil {
		return err
	}
	defer f.Close()

	writer := zip.NewWriter(f)
	defer writer.Close()

	return filepath.Walk(sourceDir, func(filePath string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}

		// Ignore directories in the sourceDir.
		if info.IsDir() {
			return nil
		}

		header, err := zip.FileInfoHeader(info)
		if err != nil {
			return err
		}

		// Don't compress, just "Store" (archive),
		// to allow direct file access and cheap unarchive.
		header.Method = zip.Store

		header.Name, err = filepath.Rel(sourceDir, filePath)
		if err != nil {
			return err
		}

		headerWriter, err := writer.CreateHeader(header)
		if err != nil {
			return err
		}

		sf, err := os.Open(filePath)
		if err != nil {
			return err
		}
		defer sf.Close()

		if _, err := io.Copy(headerWriter, sf); err != nil {
			return err
		}

		return nil
	})
}
