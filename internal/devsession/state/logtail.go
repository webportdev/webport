package state

import (
	"context"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"syscall"
	"unicode"

	"github.com/charmbracelet/x/ansi"
)

const (
	logTailBytes = 64 * 1024
	logTailLines = 200
)

// ServiceLogs returns recent saved output for service from the live state's
// captured log files, never resolving another session by worktree. Reopening
// on each call handles both rotation and truncation. Lines are plain text.
func ServiceLogs(ctx context.Context, live LiveState, service string) ([]string, error) {
	names := make([]string, 0)
	for name, path := range live.LogPaths {
		if path != "" && (name == service || strings.HasPrefix(name, service+".")) {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	if len(names) == 0 {
		return nil, fmt.Errorf("No saved logs for service %q. Logging may be disabled or unavailable for this wrapper.", service)
	}
	var lines []string
	for _, name := range names {
		if err := ctx.Err(); err != nil {
			return lines, err
		}
		tail, err := readLogTail(live.LogPaths[name])
		if len(names) > 1 {
			lines = append(lines, "── "+PlainText(name)+" ──")
		}
		if err != nil {
			lines = append(lines, PlainText(err.Error()))
			continue
		}
		lines = append(lines, tail...)
	}
	return lines, nil
}

// PlainText strips terminal sequences and control characters so process
// output cannot change a terminal or trigger clipboard writes.
func PlainText(value string) string {
	value = ansi.Strip(value)
	return strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return ' '
		}
		return r
	}, value)
}

func readLogTail(path string) ([]string, error) {
	file, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, fmt.Errorf("open log: %w", err)
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("log is not a regular file: %s", path)
	}
	start := max(int64(0), info.Size()-logTailBytes)
	if _, err := file.Seek(start, io.SeekStart); err != nil {
		return nil, err
	}
	data, err := io.ReadAll(io.LimitReader(file, logTailBytes))
	if err != nil {
		return nil, err
	}
	text := string(data)
	if start > 0 {
		if i := strings.IndexByte(text, '\n'); i >= 0 {
			text = text[i+1:]
		} else {
			text = ""
		}
	}
	// Strip terminal sequences before splitting, including multiline escape payloads.
	text = ansi.Strip(text)
	if text == "" {
		return nil, nil
	}
	lines := strings.Split(strings.TrimSuffix(text, "\n"), "\n")
	lines = lines[max(0, len(lines)-logTailLines):]
	for i := range lines {
		lines[i] = PlainText(lines[i])
	}
	return lines, nil
}
