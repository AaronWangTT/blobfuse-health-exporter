//go:build linux

package source

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func TestReportDirectoryOpensValidatedGeneration(t *testing.T) {
	path := secureTempDir(t)
	identity := testIdentity()
	reportPath := filepath.Join(path, "monitor_1234.json")
	writeReport(t, reportPath, "original", identity.CreationTime.Add(time.Second), 0o600)

	directory, err := OpenReportDirectory(path, false)
	if err != nil {
		t.Fatalf("OpenReportDirectory() error = %v", err)
	}
	t.Cleanup(func() { directory.Close() })

	report, err := directory.OpenReport(identity, 0)
	if err != nil {
		t.Fatalf("OpenReport() error = %v", err)
	}
	t.Cleanup(func() { report.Close() })
	if report.Generation.Device == 0 || report.Generation.Inode == 0 {
		t.Fatalf("Generation = %#v, want device and inode", report.Generation)
	}
	if report.Size != int64(len("original")) {
		t.Fatalf("Size = %d, want %d", report.Size, len("original"))
	}

	buffer := make([]byte, len("original"))
	if _, err := report.ReadAt(buffer, 0); err != nil && !errors.Is(err, io.EOF) {
		t.Fatalf("ReadAt() error = %v", err)
	}
	if string(buffer) != "original" {
		t.Fatalf("ReadAt() = %q, want original", buffer)
	}
}

func TestReportFileSurvivesPathReplacement(t *testing.T) {
	path := secureTempDir(t)
	identity := testIdentity()
	reportPath := filepath.Join(path, "monitor_1234.json")
	writeReport(t, reportPath, "original", identity.CreationTime.Add(time.Second), 0o600)

	directory, err := OpenReportDirectory(path, false)
	if err != nil {
		t.Fatalf("OpenReportDirectory() error = %v", err)
	}
	t.Cleanup(func() { directory.Close() })
	report, err := directory.OpenReport(identity, 0)
	if err != nil {
		t.Fatalf("OpenReport() error = %v", err)
	}
	t.Cleanup(func() { report.Close() })

	if err := os.Rename(reportPath, filepath.Join(path, "monitor_1234_1.json")); err != nil {
		t.Fatalf("Rename() error = %v", err)
	}
	writeReport(t, reportPath, "replacement", identity.CreationTime.Add(2*time.Second), 0o600)

	buffer := make([]byte, len("original"))
	if _, err := report.ReadAt(buffer, 0); err != nil && !errors.Is(err, io.EOF) {
		t.Fatalf("ReadAt() error = %v", err)
	}
	if string(buffer) != "original" {
		t.Fatalf("opened descriptor read %q, want original", buffer)
	}
}

func TestReportDirectoryRejectsSymlinks(t *testing.T) {
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
	if _, err := directory.OpenReport(identity, 0); err == nil {
		t.Fatal("OpenReport() error = nil for symlink")
	}

	linkPath := filepath.Join(filepath.Dir(path), "report-directory-link")
	if err := os.Symlink(path, linkPath); err != nil {
		t.Fatalf("Symlink() error = %v", err)
	}
	if _, err := OpenReportDirectory(linkPath, true); err == nil {
		t.Fatal("OpenReportDirectory() error = nil for symlink")
	}
}

func TestReportDirectoryPermissionOverride(t *testing.T) {
	path := secureTempDir(t)
	identity := testIdentity()
	reportPath := filepath.Join(path, "monitor_1234.json")
	writeReport(t, reportPath, "[]", identity.CreationTime.Add(time.Second), 0o640)

	strictDirectory, err := OpenReportDirectory(path, false)
	if err != nil {
		t.Fatalf("OpenReportDirectory() error = %v", err)
	}
	t.Cleanup(func() { strictDirectory.Close() })
	if _, err := strictDirectory.OpenReport(identity, 0); !errors.Is(err, ErrUnsafeSource) {
		t.Fatalf("OpenReport() error = %v, want ErrUnsafeSource", err)
	}

	directory, err := OpenReportDirectory(path, true)
	if err != nil {
		t.Fatalf("OpenReportDirectory(insecure) error = %v", err)
	}
	t.Cleanup(func() { directory.Close() })

	report, err := directory.OpenReport(identity, 0)
	if err != nil {
		t.Fatalf("OpenReport(insecure) error = %v", err)
	}
	report.Close()
}

func TestReportDirectoryRejectsNonRegularFileWithOverride(t *testing.T) {
	path := secureTempDir(t)
	if err := unix.Mkfifo(filepath.Join(path, "monitor_1234.json"), 0o600); err != nil {
		t.Fatalf("Mkfifo() error = %v", err)
	}
	directory, err := OpenReportDirectory(path, true)
	if err != nil {
		t.Fatalf("OpenReportDirectory() error = %v", err)
	}
	t.Cleanup(func() { directory.Close() })

	if _, err := directory.OpenReport(testIdentity(), 0); !errors.Is(err, ErrUnsafeSource) {
		t.Fatalf("OpenReport() error = %v, want ErrUnsafeSource", err)
	}
}

func TestReportDirectoryRejectsStaleGeneration(t *testing.T) {
	for _, test := range []struct {
		name             string
		modificationTime time.Time
	}{
		{name: "before", modificationTime: testIdentity().CreationTime.Add(-time.Nanosecond)},
		{name: "equal", modificationTime: testIdentity().CreationTime},
	} {
		t.Run(test.name, func(t *testing.T) {
			path := secureTempDir(t)
			writeReport(t, filepath.Join(path, "monitor_1234.json"), "[]", test.modificationTime, 0o600)
			directory, err := OpenReportDirectory(path, false)
			if err != nil {
				t.Fatalf("OpenReportDirectory() error = %v", err)
			}
			defer directory.Close()

			if _, err := directory.OpenReport(testIdentity(), 0); !errors.Is(err, ErrStaleGeneration) {
				t.Fatalf("OpenReport() error = %v, want ErrStaleGeneration", err)
			}
		})
	}
}

func TestValidateDirectoryStatRejectsWrongOwner(t *testing.T) {
	stat := unix.Stat_t{Mode: unix.S_IFDIR | 0o700, Uid: uint32(os.Geteuid() + 1)}
	if err := validateDirectoryStat(&stat, uint32(os.Geteuid()), false); !errors.Is(err, ErrUnsafeSource) {
		t.Fatalf("validateDirectoryStat() error = %v, want ErrUnsafeSource", err)
	}
	if err := validateDirectoryStat(&stat, uint32(os.Geteuid()), true); err != nil {
		t.Fatalf("validateDirectoryStat(insecure) error = %v", err)
	}
}

func secureTempDir(t *testing.T) string {
	t.Helper()
	path := t.TempDir()
	if err := os.Chmod(path, 0o700); err != nil {
		t.Fatalf("Chmod() error = %v", err)
	}
	return path
}

func testIdentity() ProcessIdentity {
	return ProcessIdentity{
		BootID:       "4c1f5f44-7e22-4b5a-91d9-16d2d55f5c81",
		PID:          1234,
		StartTicks:   250,
		CreationTime: time.Unix(1_700_000_000, 500_000_000).UTC(),
	}
}

func writeReport(t *testing.T, path, contents string, modificationTime time.Time, mode os.FileMode) {
	t.Helper()
	if err := os.WriteFile(path, []byte(contents), mode); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	if err := os.Chmod(path, mode); err != nil {
		t.Fatalf("Chmod() error = %v", err)
	}
	if err := os.Chtimes(path, modificationTime, modificationTime); err != nil {
		t.Fatalf("Chtimes() error = %v", err)
	}
}
