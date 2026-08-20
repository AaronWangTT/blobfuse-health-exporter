//go:build linux

package source

import (
	"errors"
	"path/filepath"
	"testing"
	"time"
)

func TestScanReportsFailsClosedForSamePIDEarlierLifetimeRetainedGeneration(t *testing.T) {
	path := secureTempDir(t)
	identity := testIdentity()
	writeReport(
		t,
		filepath.Join(path, "monitor_1234_1.json"),
		`[{"n":1}]`,
		identity.CreationTime.Add(-time.Second),
		0o600,
	)
	writeReport(
		t,
		filepath.Join(path, "monitor_1234.json"),
		`[{"n":2},`,
		identity.CreationTime.Add(time.Second),
		0o600,
	)
	directory := openTestDirectory(t, path)

	discovered, err := directory.ScanReports(identity)
	closeDiscovered(discovered)
	if !errors.Is(err, ErrStaleGeneration) {
		t.Fatalf("ScanReports() error = %v, want ErrStaleGeneration", err)
	}
}
