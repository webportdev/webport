//go:build !linux && !darwin

package secrets

import (
	"fmt"
	"os"
)

func checkUserOnly(path string, info os.FileInfo, want os.FileMode) error {
	if info.Mode().Perm() != want {
		return fmt.Errorf("%s has unsafe permissions %04o; want %04o", path, info.Mode().Perm(), want)
	}
	return nil
}
