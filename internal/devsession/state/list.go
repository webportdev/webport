package state

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
)

// ListLive returns current-user session metadata whose supervisor is still
// alive. Control credentials remain private to the caller and must not be
// rendered or exported. Stale files are ignored without modifying them.
func ListLive() ([]LiveState, error) {
	paths, err := filepath.Glob(filepath.Join(defaultDirectory(), "*.live.json"))
	if err != nil {
		return nil, err
	}
	result := make([]LiveState, 0, len(paths))
	var readErrors error
	for _, path := range paths {
		live, err := readJSON[LiveState](path)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			readErrors = errors.Join(readErrors, fmt.Errorf("read %s: %w", path, err))
			continue
		}
		if len(live.PIDs) == 0 || !processAlive(live.PIDs[0]) {
			continue
		}
		result = append(result, live)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Worktree < result[j].Worktree })
	return result, readErrors
}
