// Package logs provides prefixed terminal output, raw log mirrors, rotation,
// and bounded per-service tails.
package logs

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"github.com/webportdev/webport/internal/devsession/command"
	"github.com/webportdev/webport/internal/devsession/config"
)

type Manager struct {
	root        string
	sessionRoot string
	out         io.Writer
	color       bool
	mu          sync.Mutex
	sinks       map[string]*Sink
}

type Sink struct {
	manager *Manager
	service string
	config  *config.Logs
	files   map[string]*os.File
	paths   map[string]string
	tail    []string
	tailLen int
	mu      sync.Mutex
}

func New(root, sessionRoot string, out io.Writer, color bool) (*Manager, error) {
	if root == "" {
		return nil, errors.New("log root is required")
	}
	root, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	if sessionRoot != "" {
		sessionRoot, err = filepath.Abs(sessionRoot)
		if err != nil {
			return nil, err
		}
	}
	if out == nil {
		out = io.Discard
	}
	return &Manager{root: root, sessionRoot: sessionRoot, out: out, color: color, sinks: make(map[string]*Sink)}, nil
}

func (m *Manager) Sink(service string, settings *config.Logs) (command.OutputSink, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if existing := m.sinks[service]; existing != nil {
		return existing, nil
	}
	sink := &Sink{manager: m, service: service, config: settings, files: make(map[string]*os.File), paths: make(map[string]string)}
	if err := sink.open(); err != nil {
		return nil, err
	}
	m.sinks[service] = sink
	return sink, nil
}

func (m *Manager) Paths() map[string]string {
	m.mu.Lock()
	defer m.mu.Unlock()
	result := make(map[string]string)
	for service, sink := range m.sinks {
		for stream, path := range sink.paths {
			if stream == "combined" {
				result[service] = path
			} else {
				result[service+"."+stream] = path
			}
		}
	}
	return result
}

func (m *Manager) Tail(service string) []string {
	m.mu.Lock()
	sink := m.sinks[service]
	m.mu.Unlock()
	if sink == nil {
		return nil
	}
	sink.mu.Lock()
	defer sink.mu.Unlock()
	return append([]string(nil), sink.tail...)
}

func (m *Manager) Close() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	var combined error
	for _, sink := range m.sinks {
		if err := sink.close(); err != nil {
			combined = errors.Join(combined, err)
		}
	}
	return combined
}

func (s *Sink) open() error {
	if s.config == nil || s.config.Destination == "none" {
		return nil
	}
	var path string
	switch s.config.Destination {
	case "session", "":
		if s.manager.sessionRoot == "" {
			return errors.New("session log root is required")
		}
		path = filepath.Join(s.manager.sessionRoot, s.service+".log")
		var err error
		path, err = safePath(s.manager.sessionRoot, path)
		if err != nil {
			return err
		}
	case "file", "directory":
		if s.config.Path == "" {
			return nil
		}
		path = s.config.Path
		if s.config.Destination == "directory" {
			path = filepath.Join(path, s.service+".log")
		}
		var err error
		path, err = safePath(s.manager.root, path)
		if err != nil {
			return err
		}
	default:
		return fmt.Errorf("unsupported log destination %q", s.config.Destination)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	streams := []string{"combined"}
	if s.config.Streams == "separate" {
		streams = []string{"stdout", "stderr"}
	}
	for _, stream := range streams {
		streamPath := path
		if stream != "combined" {
			extension := filepath.Ext(path)
			streamPath = strings.TrimSuffix(path, extension) + "." + stream + extension
		}
		file, openErr := s.openFile(streamPath)
		if openErr != nil {
			_ = s.close()
			return openErr
		}
		s.files[stream] = file
		s.paths[stream] = streamPath
	}
	return nil
}

func (s *Sink) openFile(path string) (*os.File, error) {
	flags := os.O_CREATE | os.O_WRONLY
	if s.config.Mode == "append" {
		flags |= os.O_APPEND
	} else {
		flags |= os.O_TRUNC
	}
	file, err := os.OpenFile(path, flags, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open log %s: %w", path, err)
	}
	if err := file.Chmod(0o600); err != nil {
		_ = file.Close()
		return nil, err
	}
	return file, nil
}

func (s *Sink) WriteOutput(event command.OutputEvent) {
	s.mu.Lock()
	defer s.mu.Unlock()
	stream := event.Stream
	file := s.files[stream]
	if file == nil {
		file = s.files["combined"]
	}
	if file != nil {
		if s.config != nil && s.config.MaxBytes > 0 {
			if info, err := file.Stat(); err == nil && info.Size()+int64(len(event.Raw)) > s.config.MaxBytes {
				_ = s.rotate(stream)
				file = s.files[stream]
				if file == nil {
					file = s.files["combined"]
				}
			}
		}
		if file != nil {
			_, _ = file.Write(event.Raw)
		}
	}
	if event.Line != "" {
		s.tail = append(s.tail, event.Line)
		if len(s.tail) > 100 {
			s.tail = s.tail[len(s.tail)-100:]
		}
		s.tailLen += len(event.Line)
		for s.tailLen > 64*1024 && len(s.tail) > 0 {
			s.tailLen -= len(s.tail[0])
			s.tail = s.tail[1:]
		}
	}
	if event.Complete {
		prefix := "[" + s.service + "] "
		if s.manager.color {
			prefix = "\x1b[36m" + prefix + "\x1b[0m"
		}
		if event.Line != "" {
			_, _ = fmt.Fprintln(s.manager.out, prefix+event.Line)
		} else {
			_, _ = fmt.Fprintln(s.manager.out, prefix)
		}
	} else {
		_, _ = fmt.Fprint(s.manager.out, "["+s.service+"] "+event.Line)
	}
}

func (s *Sink) rotate(stream string) error {
	file := s.files[stream]
	if file == nil {
		file = s.files["combined"]
		stream = "combined"
	}
	path := s.paths[stream]
	if file == nil || path == "" {
		return nil
	}
	_ = file.Close()
	backups := 1
	if s.config != nil && s.config.Backups > 0 {
		backups = s.config.Backups
	}
	for index := backups; index >= 1; index-- {
		from := fmt.Sprintf("%s.%d", path, index)
		to := fmt.Sprintf("%s.%d", path, index+1)
		if index == backups {
			_ = os.Remove(from)
		} else {
			_ = os.Rename(from, to)
		}
	}
	_ = os.Rename(path, path+".1")
	reopened, err := s.openFile(path)
	if err != nil {
		return err
	}
	s.files[stream] = reopened
	return nil
}

func (s *Sink) close() error {
	var combined error
	for stream, file := range s.files {
		if err := file.Close(); err != nil {
			combined = errors.Join(combined, fmt.Errorf("close %s log: %w", stream, err))
		}
	}
	s.files = make(map[string]*os.File)
	return combined
}

func safePath(root, path string) (string, error) {
	if !filepath.IsAbs(path) {
		path = filepath.Join(root, path)
	}
	path, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	rel, err := filepath.Rel(root, path)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("log path %q is outside configuration root", path)
	}
	return path, nil
}

var _ = sort.Strings
