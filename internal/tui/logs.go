package tui

import (
	"context"

	"github.com/webportdev/webport/internal/devsession/state"
)

// Logs reads the captured instance's own log files.
func (b LocalBackend) Logs(ctx context.Context, live state.LiveState, service string) ([]string, error) {
	return state.ServiceLogs(ctx, live, service)
}
