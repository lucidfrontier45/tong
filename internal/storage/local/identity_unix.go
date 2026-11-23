//go:build unix

package local

import (
	"fmt"
	"io/fs"
	"syscall"
)

func fileIdentity(_ string, info fs.FileInfo) (string, error) {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return "", fmt.Errorf("no filesystem identity for %s", info.Name())
	}
	return fmt.Sprintf("file:%d:%d", stat.Dev, stat.Ino), nil
}
