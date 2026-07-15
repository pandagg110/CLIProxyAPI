//go:build windows

package loguploader

import (
	"errors"
	"os"

	"golang.org/x/sys/windows"
)

type windowsFileLock struct {
	file       *os.File
	overlapped windows.Overlapped
}

func acquireFileLock(path string) (processLock, error) {
	file, errOpen := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if errOpen != nil {
		return nil, errOpen
	}
	lock := &windowsFileLock{file: file}
	errLock := windows.LockFileEx(
		windows.Handle(file.Fd()),
		windows.LOCKFILE_EXCLUSIVE_LOCK|windows.LOCKFILE_FAIL_IMMEDIATELY,
		0,
		1,
		0,
		&lock.overlapped,
	)
	if errLock != nil {
		_ = file.Close()
		if errors.Is(errLock, windows.ERROR_LOCK_VIOLATION) {
			return nil, errFileLockBusy
		}
		return nil, errLock
	}
	return lock, nil
}

func (l *windowsFileLock) Close() error {
	errUnlock := windows.UnlockFileEx(windows.Handle(l.file.Fd()), 0, 1, 0, &l.overlapped)
	errClose := l.file.Close()
	return errors.Join(errUnlock, errClose)
}
