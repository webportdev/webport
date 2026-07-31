//go:build linux || darwin

package discovery

import (
	"bufio"
	"context"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"testing"
	"time"
)

func TestLiveProcessDiscovery(t *testing.T) {
	if os.Getenv("WEBPORT_RUN_LIVE_DISCOVERY_TEST") != "1" {
		t.Skip("set WEBPORT_RUN_LIVE_DISCOVERY_TEST=1 to exercise process and socket discovery")
	}

	command := exec.Command(os.Args[0], "-test.run=TestDiscoveryHelperProcess")
	command.Env = append(os.Environ(),
		"WEBPORT_DISCOVERY_HELPER=1",
		"WEBPORT_ROUTE=live-test:main",
		"WEBPORT_CLIENT_TOKEN=live-test-token",
	)
	stdout, err := command.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	command.Stderr = os.Stderr
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = command.Process.Kill()
		_ = command.Wait()
	})

	ready := make(chan string, 1)
	go func() {
		line, _ := bufio.NewReader(stdout).ReadString('\n')
		ready <- line
	}()
	select {
	case line := <-ready:
		if line != "ready\n" {
			t.Fatalf("helper output = %q", line)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("helper did not become ready")
	}

	scanner := Scanner{
		BaseDomain:   "example.com",
		ProbeTimeout: time.Second,
		ClientToken:  "live-test-token",
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		routes, _ := scanner.Scan(context.Background())
		for _, item := range routes {
			if item.Project == "live-test" && item.Branch == "main" {
				if item.Port == 0 || item.Domain != "live-test-main.example.com" {
					t.Fatalf("unexpected discovered route: %#v", item)
				}
				return
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("tagged helper process was not discovered")
}

func TestDiscoveryHelperProcess(t *testing.T) {
	if os.Getenv("WEBPORT_DISCOVERY_HELPER") != "1" {
		return
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	fmt.Println("ready")
	server := &http.Server{
		Handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusNoContent)
		}),
	}
	if err := server.Serve(listener); err != nil && err != http.ErrServerClosed {
		t.Fatal(err)
	}
}
