//go:build !darwin && !dragonfly && !freebsd && !linux && !netbsd && !openbsd && !solaris && !windows

package loguploader

import (
	"os"
)

type exclusiveFileLock struct {
	file *os.File
	path string
}

func acquireFileLock(path string) (processLock, error) {
	file, errOpen := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_RDWR, 0o600)
	if os.IsExist(errOpen) {
		return nil, errFileLockBusy
	}
	if errOpen != nil {
		return nil, errOpen
	}
	return &exclusiveFileLock{file: file, path: path}, nil
}

func (l *exclusiveFileLock) Close() error {
	errClose := l.file.Close()
	errRemove := os.Remove(l.path)
	if errClose != nil {
		return errClose
	}
	return errRemove
}
