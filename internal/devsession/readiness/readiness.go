// Package readiness performs bounded startup checks for configured services.
package readiness

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/webportdev/webport/internal/devsession/command"
	"github.com/webportdev/webport/internal/devsession/config"
)

type Result struct {
	Ready     bool
	Attempts  int
	LastProbe string
	LastError string
	Elapsed   time.Duration
}

type Options struct {
	Expand         func(string) (string, error)
	ProbeTimeout   time.Duration
	OverallTimeout time.Duration
	Interval       time.Duration
	HTTPClient     *http.Client
	CommandRunner  command.Runner
	WorkingDir     string
	Environment    []string
}

func Check(ctx context.Context, ready *config.Ready, options Options) (Result, error) {
	if ready == nil {
		return Result{Ready: true}, nil
	}
	if options.Expand == nil {
		options.Expand = func(value string) (string, error) { return value, nil }
	}
	if ready.HTTP != nil {
		options.OverallTimeout = durationOr(ready.HTTP.OverallTimeout, options.OverallTimeout)
		options.Interval = durationOr(ready.HTTP.Interval, options.Interval)
	}
	if options.ProbeTimeout <= 0 {
		options.ProbeTimeout = 2 * time.Second
	}
	if options.OverallTimeout <= 0 {
		options.OverallTimeout = 60 * time.Second
	}
	if options.Interval <= 0 {
		options.Interval = 500 * time.Millisecond
	}
	checks := 0
	if ready.TCP != "" {
		checks++
	}
	if ready.HTTP != nil {
		checks++
	}
	if len(ready.Command) > 0 {
		checks++
	}
	if checks != 1 {
		return Result{}, errors.New("readiness must define exactly one check")
	}
	started := time.Now()
	overall, cancel := context.WithTimeout(ctx, options.OverallTimeout)
	defer cancel()
	var result Result
	for {
		result.Attempts++
		var err error
		switch {
		case ready.TCP != "":
			result.LastProbe = "tcp " + ready.TCP
			err = checkTCP(overall, ready.TCP, options)
		case ready.HTTP != nil:
			result.LastProbe = "http " + ready.HTTP.URL
			err = checkHTTP(overall, *ready.HTTP, options)
		default:
			result.LastProbe = "command"
			err = checkCommand(overall, ready.Command, options)
		}
		result.Elapsed = time.Since(started)
		if err == nil {
			result.Ready = true
			return result, nil
		}
		result.LastError = redactError(err)
		if overall.Err() != nil {
			return result, fmt.Errorf("readiness timeout after %d attempts (%s): %s", result.Attempts, result.LastProbe, result.LastError)
		}
		timer := time.NewTimer(options.Interval)
		select {
		case <-overall.Done():
			timer.Stop()
			continue
		case <-timer.C:
		}
	}
}

func ResolveEndpoints(endpoints map[string]string, expand func(string) (string, error)) (map[string]string, error) {
	if expand == nil {
		expand = func(value string) (string, error) { return value, nil }
	}
	result := make(map[string]string, len(endpoints))
	for name, endpoint := range endpoints {
		value, err := expand(endpoint)
		if err != nil {
			return nil, fmt.Errorf("endpoint %q: %w", name, err)
		}
		result[name] = value
	}
	return result, nil
}

func checkTCP(ctx context.Context, endpoint string, options Options) error {
	endpoint, err := options.Expand(endpoint)
	if err != nil {
		return err
	}
	host, port, err := net.SplitHostPort(endpoint)
	if err != nil {
		return fmt.Errorf("invalid TCP endpoint: %w", err)
	}
	probeCtx, cancel := context.WithTimeout(ctx, options.ProbeTimeout)
	defer cancel()
	dialer := net.Dialer{}
	connection, err := dialer.DialContext(probeCtx, "tcp", net.JoinHostPort(host, port))
	if err != nil {
		return fmt.Errorf("TCP probe failed: %w", err)
	}
	return connection.Close()
}

func checkHTTP(ctx context.Context, ready config.HTTPReady, options Options) error {
	endpoint, err := options.Expand(ready.URL)
	if err != nil {
		return err
	}
	method := ready.Method
	if method == "" {
		method = http.MethodGet
	}
	probeCtx, cancel := context.WithTimeout(ctx, durationOr(ready.Timeout, options.ProbeTimeout))
	defer cancel()
	request, err := http.NewRequestWithContext(probeCtx, method, endpoint, http.NoBody)
	if err != nil {
		return fmt.Errorf("create HTTP probe: %w", err)
	}
	for name, value := range ready.Headers {
		expanded, expandErr := options.Expand(value)
		if expandErr != nil {
			return expandErr
		}
		request.Header.Set(name, expanded)
	}
	client := options.HTTPClient
	if client == nil {
		transport := http.DefaultTransport.(*http.Transport).Clone()
		transport.TLSClientConfig = &tls.Config{InsecureSkipVerify: ready.InsecureSkipVerify} // explicit schema option
		client = &http.Client{Transport: transport}
	}
	response, err := client.Do(request)
	if err != nil {
		return fmt.Errorf("HTTP probe failed: %w", err)
	}
	defer response.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 64*1024))
	if len(ready.Status) == 0 {
		if response.StatusCode >= 200 && response.StatusCode < 300 {
			return nil
		}
		return fmt.Errorf("HTTP probe returned status %d", response.StatusCode)
	}
	for _, status := range ready.Status {
		if response.StatusCode == status {
			return nil
		}
	}
	return fmt.Errorf("HTTP probe returned unexpected status %d", response.StatusCode)
}

func checkCommand(ctx context.Context, values []string, options Options) error {
	commandValues := make([]string, len(values))
	for index, value := range values {
		expanded, err := options.Expand(value)
		if err != nil {
			return fmt.Errorf("command readiness argument %d: %w", index, err)
		}
		commandValues[index] = expanded
	}
	probeCtx, cancel := context.WithTimeout(ctx, options.ProbeTimeout)
	defer cancel()
	process, err := options.CommandRunner.Start(probeCtx, command.Spec{Command: commandValues, Dir: options.WorkingDir, Env: options.Environment, StopSignal: func() os.Signal { return os.Kill }, GracePeriod: time.Millisecond})
	if err != nil {
		return fmt.Errorf("start command readiness: %w", err)
	}
	err = process.Wait()
	_ = process.Terminate(os.Kill, time.Millisecond)
	if err != nil || probeCtx.Err() != nil {
		return fmt.Errorf("command readiness exited unsuccessfully")
	}
	return nil
}

func durationOr(value config.Duration, fallback time.Duration) time.Duration {
	if value.Duration() > 0 {
		return value.Duration()
	}
	return fallback
}

func redactError(err error) string {
	if err == nil {
		return ""
	}
	message := strings.TrimSpace(err.Error())
	message = strings.ReplaceAll(message, string(os.PathSeparator)+"home"+string(os.PathSeparator), "<path>")
	return message
}
