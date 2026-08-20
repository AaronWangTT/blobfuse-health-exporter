//go:build linux

package source

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestScanReportsDiscoversOnlySelectedPID(t *testing.T) {
	path := secureTempDir(t)
	identity := testIdentity()
	for rotation, contents := range map[int]string{
		0: "current",
		4: "middle",
		9: "oldest",
	} {
		name, err := reportName(identity.PID, rotation)
		if err != nil {
			t.Fatalf("reportName() error = %v", err)
		}
		writeReport(t, filepath.Join(path, name), contents, identity.CreationTime.Add(time.Second), 0o600)
	}
	writeReport(t, filepath.Join(path, "monitor_4321.json"), "other pid", identity.CreationTime.Add(time.Second), 0o600)
	writeReport(t, filepath.Join(path, "monitor_1234_10.json"), "unsupported rotation", identity.CreationTime.Add(time.Second), 0o600)
	writeReport(t, filepath.Join(path, "notes.txt"), "unrelated", identity.CreationTime.Add(time.Second), 0o600)

	directory, err := OpenReportDirectory(path, false)
	if err != nil {
		t.Fatalf("OpenReportDirectory() error = %v", err)
	}
	t.Cleanup(func() { directory.Close() })

	discovered, err := directory.ScanReports(identity)
	if err != nil {
		t.Fatalf("ScanReports() error = %v", err)
	}
	t.Cleanup(func() { closeDiscovered(discovered) })

	got := make(map[int]bool, len(discovered))
	for _, generation := range discovered {
		got[generation.Rotation] = true
	}
	for _, rotation := range []int{0, 4, 9} {
		if !got[rotation] {
			t.Errorf("rotation %d was not discovered", rotation)
		}
	}
	if len(got) != 3 {
		t.Fatalf("discovered rotations = %v, want only 0, 4, and 9", got)
	}
}

func TestScanReportsDeduplicatesGenerationIdentity(t *testing.T) {
	path := secureTempDir(t)
	identity := testIdentity()
	current := filepath.Join(path, "monitor_1234.json")
	retained := filepath.Join(path, "monitor_1234_1.json")
	writeReport(t, current, "same generation", identity.CreationTime.Add(time.Second), 0o600)
	if err := os.Link(current, retained); err != nil {
		t.Fatalf("Link() error = %v", err)
	}

	directory, err := OpenReportDirectory(path, false)
	if err != nil {
		t.Fatalf("OpenReportDirectory() error = %v", err)
	}
	t.Cleanup(func() { directory.Close() })
	discovered, err := directory.ScanReports(identity)
	if err != nil {
		t.Fatalf("ScanReports() error = %v", err)
	}
	t.Cleanup(func() { closeDiscovered(discovered) })

	if len(discovered) != 1 {
		t.Fatalf("len(discovered) = %d, want 1", len(discovered))
	}
	if discovered[0].Rotation != 1 {
		t.Fatalf("Rotation = %d, want oldest observed position 1", discovered[0].Rotation)
	}
}

func TestScanReportsFailsClosedForMatchingSymlink(t *testing.T) {
	path := secureTempDir(t)
	identity := testIdentity()
	writeReport(t, filepath.Join(path, "target.json"), "[]", identity.CreationTime.Add(time.Second), 0o600)
	if err := os.Symlink("target.json", filepath.Join(path, "monitor_1234.json")); err != nil {
		t.Fatalf("Symlink() error = %v", err)
	}

	directory, err := OpenReportDirectory(path, false)
	if err != nil {
		t.Fatalf("OpenReportDirectory() error = %v", err)
	}
	t.Cleanup(func() { directory.Close() })
	if _, err := directory.ScanReports(identity); err == nil {
		t.Fatal("ScanReports() error = nil for matching symlink")
	}
}

func TestScanReportsRejectsClosedDirectory(t *testing.T) {
	path := secureTempDir(t)
	directory, err := OpenReportDirectory(path, false)
	if err != nil {
		t.Fatalf("OpenReportDirectory() error = %v", err)
	}
	if err := directory.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}

	if _, err := directory.ScanReports(testIdentity()); !errors.Is(err, os.ErrClosed) {
		t.Fatalf("ScanReports() error = %v, want os.ErrClosed", err)
	}
}
