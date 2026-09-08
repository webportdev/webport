package readiness

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/webportdev/webport/internal/devsession/config"
)

func TestCheckTCPAndCancellation(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	go func() {
		for {
			connection, acceptErr := listener.Accept()
			if acceptErr != nil {
				return
			}
			_ = connection.Close()
		}
	}()
	result, err := Check(context.Background(), &config.Ready{TCP: listener.Addr().String()}, Options{OverallTimeout: time.Second, Interval: time.Millisecond})
	if err != nil || !result.Ready || result.Attempts != 1 {
		t.Fatalf("TCP result = %+v, %v", result, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	result, err = Check(ctx, &config.Ready{TCP: "127.0.0.1:1"}, Options{OverallTimeout: time.Second, Interval: time.Millisecond})
	if err == nil || result.LastError == "" {
		t.Fatalf("cancelled TCP result = %+v, %v", result, err)
	}
}

func TestCheckHTTPRetriesAndExpectedStatuses(t *testing.T) {
	attempts := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts++
		if attempts < 2 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()
	result, err := Check(context.Background(), &config.Ready{HTTP: &config.HTTPReady{URL: server.URL, Status: []int{http.StatusNoContent}, Timeout: config.Duration(100 * time.Millisecond)}}, Options{Interval: time.Millisecond, OverallTimeout: time.Second})
	if err != nil || result.Attempts != 2 {
		t.Fatalf("HTTP result = %+v, %v", result, err)
	}
}

func TestCheckCommandAndEndpointExpansion(t *testing.T) {
	result, err := Check(context.Background(), &config.Ready{Command: []string{"sh", "-c", "test \"$READY_VALUE\" = yes"}}, Options{Environment: append(os.Environ(), "READY_VALUE=yes"), OverallTimeout: time.Second})
	if err != nil || !result.Ready {
		t.Fatalf("command result = %+v, %v", result, err)
	}
	endpoints, err := ResolveEndpoints(map[string]string{"api": "http://${ports.api}"}, func(value string) (string, error) {
		return strings.ReplaceAll(value, "${ports.api}", "8080"), nil
	})
	if err != nil || endpoints["api"] != "http://8080" {
		t.Fatalf("endpoints = %v, %v", endpoints, err)
	}
	_, err = Check(context.Background(), &config.Ready{Command: []string{"echo", "${secret}"}}, Options{Expand: func(string) (string, error) { return "", fmt.Errorf("sensitive value cannot be used") }, OverallTimeout: time.Second})
	if err == nil || !strings.Contains(err.Error(), "sensitive") {
		t.Fatalf("sensitive command error = %v", err)
	}
}

func TestCheckRejectsMultipleReadinessKindsAndTimesOut(t *testing.T) {
	_, err := Check(context.Background(), &config.Ready{TCP: "127.0.0.1:1", Command: []string{"true"}}, Options{})
	if err == nil || !strings.Contains(err.Error(), "exactly one") {
		t.Fatalf("multiple check error = %v", err)
	}
	result, err := Check(context.Background(), &config.Ready{TCP: "127.0.0.1:1"}, Options{OverallTimeout: 10 * time.Millisecond, Interval: time.Millisecond, ProbeTimeout: time.Millisecond})
	if err == nil || result.Ready || !strings.Contains(err.Error(), "readiness timeout") {
		t.Fatalf("timeout = %+v, %v", result, err)
	}
}

func TestHTTPConfiguredIntervalAndOverallTimeout(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusServiceUnavailable) }))
	defer server.Close()
	started := time.Now()
	result, err := Check(context.Background(), &config.Ready{HTTP: &config.HTTPReady{
		URL: server.URL, Interval: config.Duration(5 * time.Millisecond), OverallTimeout: config.Duration(80 * time.Millisecond),
	}}, Options{OverallTimeout: time.Second, Interval: time.Second})
	if err == nil || result.Attempts < 3 || time.Since(started) > 500*time.Millisecond {
		t.Fatalf("HTTP timing = %+v, %v", result, err)
	}
}

func TestCommandReadinessHasPerProbeTimeout(t *testing.T) {
	started := time.Now()
	result, err := Check(context.Background(), &config.Ready{Command: []string{"sh", "-c", "exec sleep 10"}}, Options{
		ProbeTimeout: 20 * time.Millisecond, OverallTimeout: 150 * time.Millisecond, Interval: time.Millisecond,
	})
	if err == nil || result.Attempts < 3 || time.Since(started) > time.Second {
		t.Fatalf("command timing = %+v, %v", result, err)
	}
}
