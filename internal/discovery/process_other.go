//go:build !linux && !darwin

package discovery

import (
	"context"
	"fmt"
	"runtime"
)

func scanProcesses(_ context.Context) ([]process, []error) {
	return nil, []error{fmt.Errorf("process discovery is not yet supported on %s", runtime.GOOS)}
}
