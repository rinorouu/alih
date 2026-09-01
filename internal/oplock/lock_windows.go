//go:build windows

package oplock

import (
	"errors"
	"os"
	"syscall"
)

const (
	errorSharingViolation syscall.Errno = 32
	errorLockViolation    syscall.Errno = 33
)

func openAndLock(path string) (*os.File, bool, error) {
	pathPointer, err := syscall.UTF16PtrFromString(path)
	if err != nil {
		return nil, false, err
	}
	handle, err := syscall.CreateFile(
		pathPointer,
		syscall.GENERIC_READ|syscall.GENERIC_WRITE,
		syscall.FILE_SHARE_READ,
		nil,
		syscall.OPEN_ALWAYS,
		syscall.FILE_ATTRIBUTE_NORMAL,
		0,
	)
	if err != nil {
		if errors.Is(err, errorSharingViolation) || errors.Is(err, errorLockViolation) {
			return nil, true, nil
		}
		return nil, false, err
	}
	return os.NewFile(uintptr(handle), path), false, nil
}

func unlockAndClose(file *os.File) error { return file.Close() }
