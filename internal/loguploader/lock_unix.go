//go:build darwin || dragonfly || freebsd || linux || netbsd || openbsd || solaris

package loguploader

import (
	"errors"
	"os"

	"golang.org/x/sys/unix"
)

type unixFileLock struct {
	file *os.File
}

func acquireFileLock(path string) (processLock, error) {
	file, errOpen := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if errOpen != nil {
		return nil, errOpen
	}
	if errLock := unix.Flock(int(file.Fd()), unix.LOCK_EX|unix.LOCK_NB); errLock != nil {
		_ = file.Close()
		if errors.Is(errLock, unix.EWOULDBLOCK) || errors.Is(errLock, unix.EAGAIN) {
			return nil, errFileLockBusy
		}
		return nil, errLock
	}
	return &unixFileLock{file: file}, nil
}

func (l *unixFileLock) Close() error {
	errUnlock := unix.Flock(int(l.file.Fd()), unix.LOCK_UN)
	errClose := l.file.Close()
	return errors.Join(errUnlock, errClose)
}
