//go:build linux

package source

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestDirectoryWatcherObservesChangesAfterRegistration(t *testing.T) {
	path := secureTempDir(t)
	directory, err := OpenReportDirectory(path, false)
	if err != nil {
		t.Fatalf("OpenReportDirectory() error = %v", err)
	}
	t.Cleanup(func() { directory.Close() })
	watcher, err := WatchReportDirectory(directory)
	if err != nil {
		t.Fatalf("WatchReportDirectory() error = %v", err)
	}
	t.Cleanup(func() { watcher.Close() })

	reportPath := filepath.Join(path, "monitor_1234.json")
	if err := os.WriteFile(reportPath, []byte("["), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	if err := os.Rename(reportPath, filepath.Join(path, "monitor_1234_1.json")); err != nil {
		t.Fatalf("Rename() error = %v", err)
	}

	events, err := watcher.Drain()
	if err != nil {
		t.Fatalf("Drain() error = %v", err)
	}
	if !events.Changed || events.Overflowed || events.Invalidated {
		t.Fatalf("events = %#v, want ordinary directory changes", events)
	}
}

func TestDirectoryWatcherDrainIsNonBlockingWhenEmpty(t *testing.T) {
	path := secureTempDir(t)
	directory, err := OpenReportDirectory(path, false)
	if err != nil {
		t.Fatalf("OpenReportDirectory() error = %v", err)
	}
	t.Cleanup(func() { directory.Close() })
	watcher, err := WatchReportDirectory(directory)
	if err != nil {
		t.Fatalf("WatchReportDirectory() error = %v", err)
	}
	t.Cleanup(func() { watcher.Close() })

	events, err := watcher.Drain()
	if err != nil {
		t.Fatalf("Drain() error = %v", err)
	}
	if events != (DirectoryEvents{}) {
		t.Fatalf("events = %#v, want no events", events)
	}
}

func TestDirectoryWatcherRejectsClosedDirectory(t *testing.T) {
	path := secureTempDir(t)
	directory, err := OpenReportDirectory(path, false)
	if err != nil {
		t.Fatalf("OpenReportDirectory() error = %v", err)
	}
	if err := directory.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}

	if _, err := WatchReportDirectory(directory); !errors.Is(err, os.ErrClosed) {
		t.Fatalf("WatchReportDirectory() error = %v, want os.ErrClosed", err)
	}
}

func TestDirectoryWatcherRejectsDrainAfterClose(t *testing.T) {
	path := secureTempDir(t)
	directory, err := OpenReportDirectory(path, false)
	if err != nil {
		t.Fatalf("OpenReportDirectory() error = %v", err)
	}
	t.Cleanup(func() { directory.Close() })
	watcher, err := WatchReportDirectory(directory)
	if err != nil {
		t.Fatalf("WatchReportDirectory() error = %v", err)
	}
	if err := watcher.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}

	if _, err := watcher.Drain(); !errors.Is(err, os.ErrClosed) {
		t.Fatalf("Drain() error = %v, want os.ErrClosed", err)
	}
}
