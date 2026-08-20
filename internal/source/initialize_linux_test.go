//go:build linux

package source

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

func TestInitializeSourceEstablishesPartialTailCutover(t *testing.T) {
	path := secureTempDir(t)
	identity := testIdentity()
	contents := "[{\"n\":1},\n{\"n\":2},\n{\"n\":"
	writeReport(t, filepath.Join(path, "monitor_1234.json"), contents, identity.CreationTime.Add(time.Second), 0o600)
	directory := openTestDirectory(t, path)

	var values []int
	session, err := InitializeSource(context.Background(), directory, identity, testInitializationOptions(), func(mode RecordMode, record Record) error {
		if mode != RecordModeBaseline {
			t.Fatalf("mode = %d, want RecordModeBaseline", mode)
		}
		values = append(values, decodeTestNumber(t, record))
		return nil
	})
	if err != nil {
		t.Fatalf("InitializeSource() error = %v", err)
	}
	t.Cleanup(func() { session.Close() })

	if want := []int{1, 2}; !reflect.DeepEqual(values, want) {
		t.Fatalf("values = %v, want %v", values, want)
	}
	cutover := session.Cutover()
	if len(cutover) != 1 {
		t.Fatalf("len(cutover) = %d, want 1", len(cutover))
	}
	if cutover[0].CommittedOffset <= 1 || cutover[0].CommittedOffset >= int64(len(contents)) {
		t.Fatalf("CommittedOffset = %d, want the last complete boundary", cutover[0].CommittedOffset)
	}
	if !cutover[0].HasFingerprint || cutover[0].Closed {
		t.Fatalf("cutover = %#v, want fingerprint and retained partial tail", cutover[0])
	}
	if session.Identity() != identity {
		t.Fatalf("Identity() = %#v, want %#v", session.Identity(), identity)
	}
}

func TestInitializeSourceCapturesRotationDuringBaseline(t *testing.T) {
	path := secureTempDir(t)
	identity := testIdentity()
	currentPath := filepath.Join(path, "monitor_1234.json")
	retainedPath := filepath.Join(path, "monitor_1234_1.json")
	writeReport(t, currentPath, `[{"n":1},`, identity.CreationTime.Add(time.Second), 0o600)
	directory := openTestDirectory(t, path)

	var values []int
	rotated := false
	session, err := InitializeSource(context.Background(), directory, identity, testInitializationOptions(), func(mode RecordMode, record Record) error {
		value := decodeTestNumber(t, record)
		values = append(values, value)
		if value != 1 || rotated {
			return nil
		}

		rotated = true
		if err := os.Rename(currentPath, retainedPath); err != nil {
			t.Fatalf("Rename() error = %v", err)
		}
		appendReport(t, retainedPath, `{"n":2}]`, identity.CreationTime.Add(2*time.Second))
		writeReport(t, currentPath, `[{"n":3},`, identity.CreationTime.Add(3*time.Second), 0o600)
		return nil
	})
	if err != nil {
		t.Fatalf("InitializeSource() error = %v", err)
	}
	t.Cleanup(func() { session.Close() })

	if want := []int{1, 2, 3}; !reflect.DeepEqual(values, want) {
		t.Fatalf("baseline values = %v, want %v", values, want)
	}
	cutover := session.Cutover()
	if len(cutover) != 2 {
		t.Fatalf("len(cutover) = %d, want 2", len(cutover))
	}
	if !cutover[0].Closed || cutover[1].Closed {
		t.Fatalf("cutover = %#v, want closed retained generation before open current generation", cutover)
	}
}

func TestInitializeSourceRetriesEmptyDirectoryUntilTimeout(t *testing.T) {
	path := secureTempDir(t)
	directory := openTestDirectory(t, path)
	options := testInitializationOptions()
	options.InitializationTimeout = 20 * time.Millisecond

	_, err := InitializeSource(context.Background(), directory, testIdentity(), options, func(RecordMode, Record) error {
		return nil
	})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("InitializeSource() error = %v, want context.DeadlineExceeded", err)
	}
}

func TestInitializeSourcePropagatesHandlerFailure(t *testing.T) {
	path := secureTempDir(t)
	identity := testIdentity()
	writeReport(t, filepath.Join(path, "monitor_1234.json"), `[{"n":1},`, identity.CreationTime.Add(time.Second), 0o600)
	directory := openTestDirectory(t, path)
	wantErr := errors.New("baseline failed")

	_, err := InitializeSource(context.Background(), directory, identity, testInitializationOptions(), func(RecordMode, Record) error {
		return wantErr
	})
	if !errors.Is(err, wantErr) {
		t.Fatalf("InitializeSource() error = %v, want handler error", err)
	}
}

func testInitializationOptions() InitializationOptions {
	return InitializationOptions{
		MaxRecordBytes:        1024,
		RescanInterval:        time.Millisecond,
		InitializationTimeout: time.Second,
	}
}

func openTestDirectory(t *testing.T, path string) *ReportDirectory {
	t.Helper()
	directory, err := OpenReportDirectory(path, false)
	if err != nil {
		t.Fatalf("OpenReportDirectory() error = %v", err)
	}
	t.Cleanup(func() { directory.Close() })
	return directory
}

func decodeTestNumber(t *testing.T, record Record) int {
	t.Helper()
	var value struct {
		Number int `json:"n"`
	}
	if err := record.Decode(&value); err != nil {
		t.Fatalf("Decode() error = %v", err)
	}
	return value.Number
}
