package local

import (
	"fmt"
	"io/fs"
	"syscall"
)

func fileIdentity(name string, _ fs.FileInfo) (string, error) {
	path, err := syscall.UTF16PtrFromString(name)
	if err != nil {
		return "", err
	}
	// GetFileInformationByHandle requires metadata access only. Share delete
	// as well as read/write so inspecting a file cannot block its producer.
	h, err := syscall.CreateFile(path, 0, syscall.FILE_SHARE_READ|syscall.FILE_SHARE_WRITE|syscall.FILE_SHARE_DELETE, nil, syscall.OPEN_EXISTING, syscall.FILE_FLAG_BACKUP_SEMANTICS, 0)
	if err != nil {
		return "", err
	}
	// Read-only metadata handle; Close has no writes to flush.
	defer func() { _ = syscall.CloseHandle(h) }()
	var id syscall.ByHandleFileInformation
	if err := syscall.GetFileInformationByHandle(h, &id); err != nil {
		return "", err
	}
	return fmt.Sprintf("file:%d:%d:%d", id.VolumeSerialNumber, id.FileIndexHigh, id.FileIndexLow), nil
}
