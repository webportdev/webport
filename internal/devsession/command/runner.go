// Package command runs configured commands without deciding session policy.
package command

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sync"
	"syscall"
	"time"
)

type Spec struct {
	Command     []string
	Shell       string
	ShellPath   string
	Dir         string
	Env         []string
	Sink        OutputSink
	StopSignal  func() os.Signal
	GracePeriod time.Duration
}

type OutputEvent struct {
	Stream   string
	Raw      []byte
	Line     string
	Complete bool
}

type OutputSink interface {
	WriteOutput(OutputEvent)
}

type MemorySink struct {
	mu     sync.Mutex
	events []OutputEvent
}

func (s *MemorySink) WriteOutput(event OutputEvent) {
	s.mu.Lock()
	defer s.mu.Unlock()
	event.Raw = append([]byte(nil), event.Raw...)
	s.events = append(s.events, event)
}

func (s *MemorySink) Events() []OutputEvent {
	s.mu.Lock()
	defer s.mu.Unlock()
	result := make([]OutputEvent, len(s.events))
	copy(result, s.events)
	return result
}

type Result struct {
	PID       int
	StartTime time.Time
	EndTime   time.Time
	ExitCode  int
	Signal    string
	CleanedUp bool
}

type Process struct {
	command     *exec.Cmd
	done        chan struct{}
	started     time.Time
	mu          sync.Mutex
	waitErr     error
	result      Result
	cleaned     bool
	terminateMu sync.Mutex
}

type Runner struct{}

func (Runner) Start(ctx context.Context, spec Spec) (*Process, error) {
	if ctx != nil && ctx.Err() != nil {
		return nil, ctx.Err()
	}
	cmd, err := buildCommand(spec)
	if err != nil {
		return nil, err
	}
	stdout, stdoutWriter, err := os.Pipe()
	if err != nil {
		return nil, fmt.Errorf("create stdout pipe: %w", err)
	}
	stderr, stderrWriter, err := os.Pipe()
	if err != nil {
		_ = stdout.Close()
		_ = stdoutWriter.Close()
		return nil, fmt.Errorf("create stderr pipe: %w", err)
	}
	cmd.Stdout = stdoutWriter
	cmd.Stderr = stderrWriter
	configureProcess(cmd)
	started := time.Now()
	if err := cmd.Start(); err != nil {
		_ = stdout.Close()
		_ = stdoutWriter.Close()
		_ = stderr.Close()
		_ = stderrWriter.Close()
		return nil, fmt.Errorf("start command: %w", err)
	}
	// The child owns the duplicated write ends. Closing the parent's copies
	// leaves the readers responsible for draining all buffered output without
	// relying on os/exec's StdoutPipe/Wait close ordering.
	_ = stdoutWriter.Close()
	_ = stderrWriter.Close()
	process := &Process{command: cmd, done: make(chan struct{}), started: started, result: Result{PID: cmd.Process.Pid, StartTime: started}}
	var output sync.WaitGroup
	output.Add(2)
	go func() { defer output.Done(); defer stdout.Close(); capturePipe("stdout", stdout, spec.Sink) }()
	go func() { defer output.Done(); defer stderr.Close(); capturePipe("stderr", stderr, spec.Sink) }()
	go func() {
		waitErr := cmd.Wait()
		process.mu.Lock()
		process.waitErr = waitErr
		process.result.EndTime = time.Now()
		process.result.ExitCode, process.result.Signal = status(cmd.ProcessState)
		process.mu.Unlock()
		// A leader may exit while descendants still hold its output pipes.
		// Bound draining; lifecycle cleanup still gives the entire process
		// group its configured signal and grace period.
		drained := make(chan struct{})
		go func() { output.Wait(); close(drained) }()
		select {
		case <-drained:
		case <-time.After(100 * time.Millisecond):
			_ = stdout.Close()
			_ = stderr.Close()
			<-drained
		}
		close(process.done)
	}()
	if ctx != nil {
		go func() {
			select {
			case <-ctx.Done():
				sig := os.Signal(syscall.SIGTERM)
				if spec.StopSignal != nil {
					sig = spec.StopSignal()
				}
				_ = process.Terminate(sig, spec.GracePeriod)
			case <-process.done:
			}
		}()
	}
	return process, nil
}

func (p *Process) Done() <-chan struct{} { return p.done }

// Snapshot returns process metadata without waiting for termination.
func (p *Process) Snapshot() Result {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.result
}

func (p *Process) Wait() error {
	<-p.done
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.waitErr
}

func (p *Process) Result() Result {
	<-p.done
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.result
}

func (p *Process) Terminate(signal os.Signal, grace time.Duration) error {
	if p == nil || p.command == nil || p.command.Process == nil {
		return nil
	}
	p.terminateMu.Lock()
	defer p.terminateMu.Unlock()
	if p.cleaned {
		return p.Wait()
	}
	if grace <= 0 {
		grace = 2 * time.Second
	}
	signalErr := signalProcessGroup(p.command, signal)
	deadline := time.NewTimer(grace)
	defer deadline.Stop()
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for processGroupAlive(p.command) {
		select {
		case <-deadline.C:
			killErr := signalProcessGroup(p.command, syscall.SIGKILL)
			p.mu.Lock()
			p.cleaned = true
			p.result.CleanedUp = true
			p.mu.Unlock()
			return errors.Join(p.Wait(), signalErr, killErr)
		case <-ticker.C:
		}
	}
	p.mu.Lock()
	p.cleaned = true
	p.result.CleanedUp = true
	p.mu.Unlock()
	return errors.Join(p.Wait(), signalErr)
}

func buildCommand(spec Spec) (*exec.Cmd, error) {
	if len(spec.Command) == 0 && spec.Shell == "" {
		return nil, errors.New("command or shell is required")
	}
	if len(spec.Command) > 0 && spec.Shell != "" {
		return nil, errors.New("command and shell are mutually exclusive")
	}
	var command *exec.Cmd
	if spec.Shell != "" {
		shellPath := spec.ShellPath
		if shellPath == "" {
			shellPath = "/bin/sh"
		}
		command = exec.Command(shellPath, "-c", spec.Shell)
	} else {
		command = exec.Command(spec.Command[0], spec.Command[1:]...)
	}
	command.Dir = spec.Dir
	command.Env = append([]string(nil), spec.Env...)
	return command, nil
}

func capturePipe(stream string, reader io.Reader, sink OutputSink) {
	if sink == nil {
		_, _ = io.Copy(io.Discard, reader)
		return
	}
	buffer := make([]byte, 32*1024)
	pending := make([]byte, 0, len(buffer))
	for {
		count, err := reader.Read(buffer)
		if count > 0 {
			pending = append(pending, buffer[:count]...)
			for {
				newline := indexByte(pending, '\n')
				if newline < 0 {
					break
				}
				raw := append([]byte(nil), pending[:newline+1]...)
				line := string(pending[:newline])
				if len(line) > 0 && line[len(line)-1] == '\r' {
					line = line[:len(line)-1]
				}
				sink.WriteOutput(OutputEvent{Stream: stream, Raw: raw, Line: line, Complete: true})
				pending = pending[newline+1:]
			}
		}
		if err != nil {
			if len(pending) > 0 {
				sink.WriteOutput(OutputEvent{Stream: stream, Raw: append([]byte(nil), pending...), Line: string(pending), Complete: false})
			}
			return
		}
	}
}

func indexByte(value []byte, target byte) int {
	for index, item := range value {
		if item == target {
			return index
		}
	}
	return -1
}
