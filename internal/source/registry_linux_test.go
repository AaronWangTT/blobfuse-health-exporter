//go:build linux

package source

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestGenerationRegistryRetainsDescriptorsAcrossRotation(t *testing.T) {
	path := secureTempDir(t)
	identity := testIdentity()
	currentPath := filepath.Join(path, "monitor_1234.json")
	writeReport(t, currentPath, `[{"n":1},`, identity.CreationTime.Add(time.Second), 0o600)

	directory, err := OpenReportDirectory(path, false)
	if err != nil {
		t.Fatalf("OpenReportDirectory() error = %v", err)
	}
	t.Cleanup(func() { directory.Close() })
	registry, err := newGenerationRegistry(1024)
	if err != nil {
		t.Fatalf("newGenerationRegistry() error = %v", err)
	}
	t.Cleanup(func() { registry.close() })

	firstScan, err := directory.ScanReports(identity)
	if err != nil {
		t.Fatalf("first ScanReports() error = %v", err)
	}
	if registered, err := registry.register(firstScan); err != nil || registered != 1 {
		t.Fatalf("first register() = %d, %v; want 1, nil", registered, err)
	}
	firstReader := registry.oldestFirst()[0]
	firstGeneration := firstReader.report.Generation

	retainedPath := filepath.Join(path, "monitor_1234_1.json")
	if err := os.Rename(currentPath, retainedPath); err != nil {
		t.Fatalf("Rename() error = %v", err)
	}
	appendReport(t, retainedPath, `{"n":2}]`, identity.CreationTime.Add(2*time.Second))
	writeReport(t, currentPath, `[{"n":3},`, identity.CreationTime.Add(3*time.Second), 0o600)

	secondScan, err := directory.ScanReports(identity)
	if err != nil {
		t.Fatalf("second ScanReports() error = %v", err)
	}
	if registered, err := registry.register(secondScan); err != nil || registered != 1 {
		t.Fatalf("second register() = %d, %v; want 1, nil", registered, err)
	}
	readers := registry.oldestFirst()
	if len(readers) != 2 {
		t.Fatalf("len(readers) = %d, want 2", len(readers))
	}
	if readers[0] != firstReader || readers[0].report.Generation != firstGeneration || readers[0].rotation != 1 {
		t.Fatalf("old reader was not retained across rotation: %#v", readers[0])
	}
	if readers[1].rotation != 0 {
		t.Fatalf("new reader rotation = %d, want 0", readers[1].rotation)
	}

	var values []int
	if _, err := readers[0].drain(func(record Record) error {
		var value struct {
			Number int `json:"n"`
		}
		if err := record.Decode(&value); err != nil {
			return err
		}
		values = append(values, value.Number)
		return nil
	}); err != nil {
		t.Fatalf("old reader drain() error = %v", err)
	}
	if len(values) != 2 || values[0] != 1 || values[1] != 2 {
		t.Fatalf("old descriptor values = %v, want [1 2]", values)
	}
}

func TestGenerationRegistryClosesDuplicateScanDescriptors(t *testing.T) {
	path := secureTempDir(t)
	identity := testIdentity()
	writeReport(t, filepath.Join(path, "monitor_1234.json"), "[", identity.CreationTime.Add(time.Second), 0o600)
	directory, err := OpenReportDirectory(path, false)
	if err != nil {
		t.Fatalf("OpenReportDirectory() error = %v", err)
	}
	t.Cleanup(func() { directory.Close() })
	registry, err := newGenerationRegistry(1024)
	if err != nil {
		t.Fatalf("newGenerationRegistry() error = %v", err)
	}
	t.Cleanup(func() { registry.close() })

	firstScan, err := directory.ScanReports(identity)
	if err != nil {
		t.Fatalf("first ScanReports() error = %v", err)
	}
	if _, err := registry.register(firstScan); err != nil {
		t.Fatalf("first register() error = %v", err)
	}
	secondScan, err := directory.ScanReports(identity)
	if err != nil {
		t.Fatalf("second ScanReports() error = %v", err)
	}
	duplicateReport := secondScan[0].Report
	if registered, err := registry.register(secondScan); err != nil || registered != 0 {
		t.Fatalf("second register() = %d, %v; want 0, nil", registered, err)
	}

	if _, err := duplicateReport.ReadAt(make([]byte, 1), 0); !errors.Is(err, os.ErrClosed) {
		t.Fatalf("duplicate ReadAt() error = %v, want os.ErrClosed", err)
	}
	if len(registry.readers) != 1 {
		t.Fatalf("len(registry.readers) = %d, want 1", len(registry.readers))
	}
}

func TestGenerationRegistryRetainsGenerationAfterPathRemoval(t *testing.T) {
	path := secureTempDir(t)
	identity := testIdentity()
	reportPath := filepath.Join(path, "monitor_1234.json")
	writeReport(t, reportPath, "[", identity.CreationTime.Add(time.Second), 0o600)
	directory, err := OpenReportDirectory(path, false)
	if err != nil {
		t.Fatalf("OpenReportDirectory() error = %v", err)
	}
	t.Cleanup(func() { directory.Close() })
	registry, err := newGenerationRegistry(1024)
	if err != nil {
		t.Fatalf("newGenerationRegistry() error = %v", err)
	}
	t.Cleanup(func() { registry.close() })

	scan, err := directory.ScanReports(identity)
	if err != nil {
		t.Fatalf("ScanReports() error = %v", err)
	}
	if _, err := registry.register(scan); err != nil {
		t.Fatalf("register() error = %v", err)
	}
	if err := os.Remove(reportPath); err != nil {
		t.Fatalf("Remove() error = %v", err)
	}
	emptyScan, err := directory.ScanReports(identity)
	if err != nil {
		t.Fatalf("empty ScanReports() error = %v", err)
	}
	if _, err := registry.register(emptyScan); err != nil {
		t.Fatalf("empty register() error = %v", err)
	}
	if len(registry.readers) != 1 {
		t.Fatalf("len(registry.readers) = %d, want retained descriptor", len(registry.readers))
	}
}
