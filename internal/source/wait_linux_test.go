//go:build linux

package source

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestDirectoryWatcherWaitObservesPendingEvent(t *testing.T) {
	path := secureTempDir(t)
	directory := openTestDirectory(t, path)
	watcher, err := WatchReportDirectory(directory)
	if err != nil {
		t.Fatalf("WatchReportDirectory() error = %v", err)
	}
	t.Cleanup(func() { watcher.Close() })
	if err := os.WriteFile(filepath.Join(path, "monitor_1234.json"), []byte("["), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	changed, err := watcher.Wait(context.Background(), time.Second)
	if err != nil {
		t.Fatalf("Wait() error = %v", err)
	}
	if !changed {
		t.Fatal("Wait() changed = false, want pending event")
	}
	events, err := watcher.Drain()
	if err != nil {
		t.Fatalf("Drain() error = %v", err)
	}
	if !events.Changed {
		t.Fatal("Wait() consumed the pending event")
	}
}

func TestDirectoryWatcherWaitReturnsAtDeadline(t *testing.T) {
	path := secureTempDir(t)
	directory := openTestDirectory(t, path)
	watcher, err := WatchReportDirectory(directory)
	if err != nil {
		t.Fatalf("WatchReportDirectory() error = %v", err)
	}
	t.Cleanup(func() { watcher.Close() })

	started := time.Now()
	changed, err := watcher.Wait(context.Background(), 5*time.Millisecond)
	if err != nil {
		t.Fatalf("Wait() error = %v", err)
	}
	if changed {
		t.Fatal("Wait() changed = true without an event")
	}
	if elapsed := time.Since(started); elapsed < 4*time.Millisecond {
		t.Fatalf("Wait() returned too early after %v", elapsed)
	}
}

func TestDirectoryWatcherWaitHonorsCancellation(t *testing.T) {
	path := secureTempDir(t)
	directory := openTestDirectory(t, path)
	watcher, err := WatchReportDirectory(directory)
	if err != nil {
		t.Fatalf("WatchReportDirectory() error = %v", err)
	}
	t.Cleanup(func() { watcher.Close() })
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if _, err := watcher.Wait(ctx, time.Second); !errors.Is(err, context.Canceled) {
		t.Fatalf("Wait() error = %v, want context.Canceled", err)
	}
}
