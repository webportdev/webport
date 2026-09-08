//go:build linux || darwin

package secrets

import (
	"fmt"
	"os"
	"syscall"
)

func checkUserOnly(path string, info os.FileInfo, want os.FileMode) error {
	if info.Mode().Perm() != want {
		return fmt.Errorf("%s has unsafe permissions %04o; want %04o", path, info.Mode().Perm(), want)
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return fmt.Errorf("%s ownership metadata is unavailable", path)
	}
	if uint64(stat.Uid) != uint64(os.Getuid()) {
		return fmt.Errorf("%s is not owned by the current user", path)
	}
	return nil
}
