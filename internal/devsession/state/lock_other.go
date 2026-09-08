//go:build !linux && !darwin

package state

import (
	"os"
)

type fileLock struct{ file *os.File }

func acquireFileLock(path string) (*fileLock, error) {
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	return &fileLock{file: file}, nil
}

func (l *fileLock) Close() error { return l.file.Close() }
