package gok

import (
	"os"

	"github.com/google/renameio/v2/maybe"
)

func replaceFile(path string, content []byte, perm os.FileMode) error {
	return maybe.WriteFile(path, content, perm)
}
