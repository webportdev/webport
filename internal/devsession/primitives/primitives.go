// Package primitives contains the small injectable boundaries shared by
// configuration planning and session supervision.
package primitives

import (
	"context"
	"io"
	"os"
	"time"
)

type Clock interface {
	Now() time.Time
	After(time.Duration) <-chan time.Time
}

type Timer interface {
	C() <-chan time.Time
	Stop() bool
}

type Ticker interface {
	C() <-chan time.Time
	Stop()
}

type PortProber interface {
	Probe(context.Context, string, int) error
}

type RandomSource interface {
	io.Reader
}

type FileSystem interface {
	Stat(string) (os.FileInfo, error)
	ReadFile(string) ([]byte, error)
	WriteFile(string, []byte, os.FileMode) error
	Remove(string) error
	MkdirAll(string, os.FileMode) error
	Rename(string, string) error
}

type CommandLookup interface {
	LookPath(string) (string, error)
}
