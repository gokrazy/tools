//go:build !windows

package gok

import (
	"os"

	"github.com/google/renameio/v2"
)

func replaceFile(path string, content []byte, perm os.FileMode) error {
	return renameio.WriteFile(path, content, perm, renameio.WithExistingPermissions())
}
