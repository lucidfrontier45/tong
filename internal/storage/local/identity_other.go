//go:build !unix && !windows

package local

import (
	"errors"
	"io/fs"
	"path/filepath"
)

// CheckSourceRemoval refuses destructive moves without stable file IDs.
func (*endpoint) CheckSourceRemoval() error {
	return errors.New("local source removal requires filesystem identity support on this platform")
}

func fileIdentity(name string, _ fs.FileInfo) (string, error) {
	return filepath.EvalSymlinks(name)
}
