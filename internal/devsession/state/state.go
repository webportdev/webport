// Package state stores live session metadata and provides its local control
// transport. Secrets and control tokens are deliberately excluded from files.
package state

import (
	"bufio"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

var ErrLocked = errors.New("development session is already active")

type Store struct {
	WorktreeRoot string
	Directory    string
	LivePath     string
	LastPath     string
	LockPath     string
	lock         *fileLock
	mu           sync.Mutex
}

type LiveState struct {
	SchemaVersion int                     `json:"schema_version"`
	SessionID     string                  `json:"session_id"`
	Worktree      string                  `json:"worktree"`
	Profile       string                  `json:"profile"`
	ControlPath   string                  `json:"control_path,omitempty"`
	ControlToken  string                  `json:"control_token,omitempty"`
	StartedAt     time.Time               `json:"started_at"`
	Ports         map[string]int          `json:"ports,omitempty"`
	Services      map[string]ServiceState `json:"services,omitempty"`
	Routes        map[string]RouteState   `json:"routes,omitempty"`
	PIDs          []ProcessIdentity       `json:"pids,omitempty"`
	Endpoints     map[string]string       `json:"endpoints,omitempty"`
	LogPaths      map[string]string       `json:"log_paths,omitempty"`
	Exports       map[string]string       `json:"exports,omitempty"`
	LastError     string                  `json:"last_error,omitempty"`
}

type ServiceState struct {
	State     string    `json:"state"`
	PID       int       `json:"pid,omitempty"`
	Ready     bool      `json:"ready"`
	StartedAt time.Time `json:"started_at,omitempty"`
	EndedAt   time.Time `json:"ended_at,omitempty"`
	LastError string    `json:"last_error,omitempty"`
}

type RouteState struct {
	State    string `json:"state"`
	Project  string `json:"project"`
	Branch   string `json:"branch"`
	Host     string `json:"host,omitempty"`
	URL      string `json:"url,omitempty"`
	Optional bool   `json:"optional"`
}

type ProcessIdentity struct {
	PID       int    `json:"pid"`
	StartTime string `json:"start_time,omitempty"`
}

type LastSession struct {
	SchemaVersion int                   `json:"schema_version"`
	SessionID     string                `json:"session_id"`
	Worktree      string                `json:"worktree"`
	Profile       string                `json:"profile"`
	StartedAt     time.Time             `json:"started_at"`
	StoppedAt     time.Time             `json:"stopped_at"`
	FinalStates   map[string]string     `json:"final_states,omitempty"`
	Initiating    string                `json:"initiating,omitempty"`
	ExitCode      int                   `json:"exit_code,omitempty"`
	Signal        string                `json:"signal,omitempty"`
	CleanupErrors []string              `json:"cleanup_errors,omitempty"`
	Endpoints     map[string]string     `json:"endpoints,omitempty"`
	LogPaths      map[string]string     `json:"log_paths,omitempty"`
	Routes        map[string]RouteState `json:"routes,omitempty"`
}

func NewStore(worktreeRoot, directory string) (Store, error) {
	if strings.TrimSpace(worktreeRoot) == "" {
		return Store{}, errors.New("worktree root is required")
	}
	root, err := filepath.Abs(worktreeRoot)
	if err != nil {
		return Store{}, err
	}
	if directory == "" {
		directory = defaultDirectory()
	}
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return Store{}, fmt.Errorf("create runtime state directory: %w", err)
	}
	digest := sha256.Sum256([]byte(root))
	prefix := filepath.Join(directory, fmt.Sprintf("%x", digest[:16]))
	return Store{WorktreeRoot: root, Directory: directory, LivePath: prefix + ".live.json", LastPath: prefix + ".last.json", LockPath: prefix + ".lock"}, nil
}

func (s *Store) Acquire() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.lock != nil {
		return nil
	}
	lock, err := acquireFileLock(s.LockPath)
	if err != nil {
		if errors.Is(err, ErrLocked) {
			return &lockedError{Path: s.LivePath}
		}
		return err
	}
	if _, statErr := os.Stat(s.LivePath); statErr == nil {
		live, readErr := s.ReadLive()
		if readErr != nil {
			_ = lock.Close()
			return fmt.Errorf("inspect existing live session state: %w", readErr)
		}
		for _, process := range live.PIDs {
			if processAlive(process) {
				_ = lock.Close()
				return &lockedError{Path: s.LivePath}
			}
		}
		if removeErr := os.Remove(s.LivePath); removeErr != nil && !errors.Is(removeErr, os.ErrNotExist) {
			_ = lock.Close()
			return fmt.Errorf("remove stale live session state: %w", removeErr)
		}
	} else if !errors.Is(statErr, os.ErrNotExist) {
		_ = lock.Close()
		return statErr
	}
	s.lock = lock
	return nil
}

func (s *Store) Release() error {
	s.mu.Lock()
	lock := s.lock
	s.lock = nil
	s.mu.Unlock()
	if lock == nil {
		return nil
	}
	return lock.Close()
}

func (s *Store) WriteLive(value LiveState) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.lock == nil {
		return errors.New("session state is not locked")
	}
	value.SchemaVersion = 1
	return writeJSONAtomic(s.LivePath, value)
}

func (s *Store) RemoveLive() error {
	if err := os.Remove(s.LivePath); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

func (s Store) WriteLast(value LastSession) error {
	value.SchemaVersion = 1
	return writeJSONAtomic(s.LastPath, value)
}

func (s Store) ReadLive() (LiveState, error)   { return readJSON[LiveState](s.LivePath) }
func (s Store) ReadLast() (LastSession, error) { return readJSON[LastSession](s.LastPath) }

type lockedError struct{ Path string }

func (e *lockedError) Error() string { return fmt.Sprintf("%v; live state: %s", ErrLocked, e.Path) }
func (e *lockedError) Unwrap() error { return ErrLocked }

type Request struct {
	SchemaVersion int            `json:"schema_version"`
	Token         string         `json:"token"`
	Operation     string         `json:"operation"`
	Payload       map[string]any `json:"payload,omitempty"`
}

type Response struct {
	SchemaVersion int            `json:"schema_version"`
	OK            bool           `json:"ok"`
	Status        int            `json:"status,omitempty"`
	Error         string         `json:"error,omitempty"`
	Payload       map[string]any `json:"payload,omitempty"`
}

type Handler func(context.Context, Request) Response

type ControlServer struct {
	path     string
	token    string
	listener net.Listener
	handler  Handler
	done     chan struct{}
}

func StartControl(path string, handler Handler) (*ControlServer, error) {
	if handler == nil {
		return nil, errors.New("control handler is required")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	listener, err := net.Listen("unix", path)
	if err != nil {
		return nil, fmt.Errorf("listen on control socket: %w", err)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		_ = listener.Close()
		return nil, err
	}
	token, err := controlToken()
	if err != nil {
		_ = listener.Close()
		return nil, err
	}
	server := &ControlServer{path: path, token: token, listener: listener, handler: handler, done: make(chan struct{})}
	go server.serve()
	return server, nil
}

func (s *ControlServer) Path() string  { return s.path }
func (s *ControlServer) Token() string { return s.token }

func (s *ControlServer) Close() error {
	if err := s.listener.Close(); err != nil && !errors.Is(err, net.ErrClosed) {
		return err
	}
	<-s.done
	if err := os.Remove(s.path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

func (s *ControlServer) serve() {
	defer close(s.done)
	for {
		connection, err := s.listener.Accept()
		if err != nil {
			return
		}
		go s.handle(connection)
	}
}

func (s *ControlServer) handle(connection net.Conn) {
	defer connection.Close()
	decoder := json.NewDecoder(bufio.NewReader(connection))
	decoder.DisallowUnknownFields()
	var request Request
	if err := decoder.Decode(&request); err != nil {
		_ = json.NewEncoder(connection).Encode(Response{SchemaVersion: 1, Status: 400, Error: "invalid control request"})
		return
	}
	if request.SchemaVersion != 1 || request.Token != s.token {
		_ = json.NewEncoder(connection).Encode(Response{SchemaVersion: 1, Status: 401, Error: "unauthorized control request"})
		return
	}
	response := s.handler(context.Background(), request)
	response.SchemaVersion = 1
	_ = json.NewEncoder(connection).Encode(response)
}

func Dial(ctx context.Context, path, token string, request Request) (Response, error) {
	var dialer net.Dialer
	connection, err := dialer.DialContext(ctx, "unix", path)
	if err != nil {
		return Response{}, err
	}
	defer connection.Close()
	request.SchemaVersion = 1
	request.Token = token
	if err := json.NewEncoder(connection).Encode(request); err != nil {
		return Response{}, err
	}
	var response Response
	if err := json.NewDecoder(connection).Decode(&response); err != nil {
		return Response{}, err
	}
	return response, nil
}

func writeJSONAtomic(path string, value any) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	encoded, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	temporary, err := os.CreateTemp(filepath.Dir(path), ".webport-state-*")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if err := temporary.Chmod(0o600); err != nil {
		_ = temporary.Close()
		return err
	}
	if _, err := temporary.Write(encoded); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	if err := os.Rename(temporaryPath, path); err != nil {
		return err
	}
	return nil
}

func readJSON[T any](path string) (T, error) {
	var value T
	file, err := os.Open(path)
	if err != nil {
		return value, err
	}
	defer file.Close()
	decoder := json.NewDecoder(file)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&value); err != nil {
		return value, err
	}
	return value, nil
}

func defaultDirectory() string {
	if runtime := os.Getenv("XDG_RUNTIME_DIR"); runtime != "" {
		return filepath.Join(runtime, "webport")
	}
	cache, err := os.UserCacheDir()
	if err != nil {
		return filepath.Join(os.TempDir(), "webport-runtime")
	}
	return filepath.Join(cache, "webport", "runtime")
}

func controlToken() (string, error) {
	var data [24]byte
	if _, err := io.ReadFull(rand.Reader, data[:]); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(data[:]), nil
}
