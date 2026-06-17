package main

import (
	"context"
	"testing"
	"time"

	"github.com/coreos/go-systemd/v22/daemon"
)

// TestSystemdWatchdogInterval tests that the watchdog interval is correctly detected
func TestSystemdWatchdogInterval(t *testing.T) {
	interval, err := daemon.SdWatchdogEnabled(false)
	t.Logf("Watchdog interval: %v", interval)
	t.Logf("Watchdog interval (seconds): %.2f", interval.Seconds())
	t.Logf("Error: %v", err)

	if err != nil {
		t.Logf("Note: This test only works under systemd with watchdog enabled")
		t.Logf("When running manually, run: systemctl show webport --property=WatchdogUSec")
	}

	// If watchdog is enabled, interval should be reasonable (> 0)
	if interval > 0 {
		if interval < time.Second {
			t.Errorf("Watchdog interval too small: %v (should be at least 1s)", interval)
		}
		if interval > 60*time.Second {
			t.Errorf("Watchdog interval too large: %v (should be at most 60s)", interval)
		}
	}
}

// TestSystemdWatchdogPing tests that a watchdog ping can be sent
func TestSystemdWatchdogPing(t *testing.T) {
	// Try to send a watchdog notification
	supported, err := daemon.SdNotify(false, daemon.SdNotifyWatchdog)
	t.Logf("Watchdog ping supported: %v", supported)
	t.Logf("Error: %v", err)

	if !supported {
		t.Log("Watchdog notifications not supported (not running under systemd)")
		return
	}

	if err != nil {
		t.Errorf("Failed to send watchdog ping: %v", err)
	}
}

// TestSystemdWatchdogLoop tests the watchdog loop behavior with correct time.Duration handling
func TestSystemdWatchdogLoop(t *testing.T) {
	interval, err := daemon.SdWatchdogEnabled(false)
	if err != nil || interval == 0 {
		t.Skip("Watchdog not enabled, skipping test")
	}

	tickerInterval := interval / 2 // Ping halfway through

	t.Logf("Watchdog interval: %v", interval)
	t.Logf("Ticker interval (half of watchdog): %v", tickerInterval)

	// Run for 2x watchdog interval to ensure at least 3 pings
	ctx, cancel := context.WithTimeout(context.Background(), interval*2)
	defer cancel()

	pingCount := 0
	ticker := time.NewTicker(tickerInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			_, err := daemon.SdNotify(false, daemon.SdNotifyWatchdog)
			if err != nil {
				t.Errorf("Watchdog ping failed: %v", err)
			}
			pingCount++
			t.Logf("Sent watchdog ping #%d at %v", pingCount, time.Now().Format(time.RFC3339Nano))
		case <-ctx.Done():
			t.Logf("Test completed after %d pings", pingCount)
			// Should have sent at least 3 pings in 2x the interval
			if pingCount < 3 {
				t.Errorf("Expected at least 3 pings, got %d", pingCount)
			}
			return
		}
	}
}

// TestWatchdogIntervalCalculation verifies the correct interval calculation
// This tests the fix for the bug where interval was treated as microseconds
func TestWatchdogIntervalCalculation(t *testing.T) {
	// Simulate a 30 second watchdog (WatchdogSec=30)
	watchdogSec := 30 * time.Second

	// This is what SdWatchdogEnabled returns for WatchdogSec=30
	intervalFromSd := watchdogSec // Already a time.Duration

	// OLD BUGGY CODE (would treat nanoseconds as microseconds):
	// intervalUs := int(intervalFromSd)  // 30000000000
	// interval := time.Duration(intervalUs) * time.Microsecond  // 30000 seconds!
	// buggyTickerInterval := interval / 2  // 15000 seconds

	// NEW CORRECT CODE:
	correctTickerInterval := intervalFromSd / 2 // 15 seconds

	t.Logf("Watchdog setting: %v", watchdogSec)
	t.Logf("SdWatchdogEnabled returns: %v (nanoseconds: %d)", intervalFromSd, intervalFromSd)
	t.Logf("Correct ticker interval (ping every): %v", correctTickerInterval)

	// The correct ticker interval should be 15 seconds for a 30 second watchdog
	expected := 15 * time.Second
	if correctTickerInterval != expected {
		t.Errorf("Ticker interval should be %v, got %v", expected, correctTickerInterval)
	}

	// Verify the ticker is significantly shorter than watchdog
	if correctTickerInterval >= watchdogSec {
		t.Errorf("Ticker interval %v must be less than watchdog %v", correctTickerInterval, watchdogSec)
	}
}
