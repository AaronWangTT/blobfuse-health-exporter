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

func TestDrainSourceProcessesFinalRecordsAndReportsUncleanGeneration(t *testing.T) {
	path := secureTempDir(t)
	identity := testIdentity()
	currentPath := filepath.Join(path, "monitor_1234.json")
	retainedPath := filepath.Join(path, "monitor_1234_1.json")
	writeReport(t, currentPath, `[{"n":1},`, identity.CreationTime.Add(time.Second), 0o600)
	directory := openTestDirectory(t, path)

	var liveValues []int
	session, err := InitializeSource(context.Background(), directory, identity, testInitializationOptions(), func(mode RecordMode, record Record) error {
		if mode == RecordModeLive {
			liveValues = append(liveValues, decodeTestNumber(t, record))
		}
		return nil
	})
	if err != nil {
		t.Fatalf("InitializeSource() error = %v", err)
	}
	t.Cleanup(func() { session.Close() })

	if err := os.Rename(currentPath, retainedPath); err != nil {
		t.Fatalf("Rename() error = %v", err)
	}
	appendReport(t, retainedPath, `{"n":2}]`, identity.CreationTime.Add(2*time.Second))
	writeReport(t, currentPath, "[", identity.CreationTime.Add(3*time.Second), 0o600)

	result, err := session.DrainSource(context.Background(), DrainOptions{
		Timeout:        10 * time.Millisecond,
		RescanInterval: time.Millisecond,
	})
	if err != nil {
		t.Fatalf("DrainSource() error = %v", err)
	}
	if result.Records != 1 || result.NewGenerations != 1 {
		t.Fatalf("result = %#v, want one final record and one new generation", result)
	}
	if !reflect.DeepEqual(liveValues, []int{2}) {
		t.Fatalf("live values = %v, want [2]", liveValues)
	}
	if len(result.UncleanGenerations) != 1 || result.UncleanGenerations[0].Closed {
		t.Fatalf("unclean generations = %#v, want only the open current generation", result.UncleanGenerations)
	}
}

func TestDrainSourceHonorsParentCancellation(t *testing.T) {
	path := secureTempDir(t)
	identity := testIdentity()
	writeReport(t, filepath.Join(path, "monitor_1234.json"), "[", identity.CreationTime.Add(time.Second), 0o600)
	directory := openTestDirectory(t, path)
	session, err := InitializeSource(context.Background(), directory, identity, testInitializationOptions(), func(RecordMode, Record) error {
		return nil
	})
	if err != nil {
		t.Fatalf("InitializeSource() error = %v", err)
	}
	t.Cleanup(func() { session.Close() })

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = session.DrainSource(ctx, DrainOptions{
		Timeout:        time.Second,
		RescanInterval: time.Millisecond,
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("DrainSource() error = %v, want context.Canceled", err)
	}
}

func TestDrainSourcePropagatesLiveHandlerFailure(t *testing.T) {
	path := secureTempDir(t)
	identity := testIdentity()
	reportPath := filepath.Join(path, "monitor_1234.json")
	writeReport(t, reportPath, `[{"n":1},`, identity.CreationTime.Add(time.Second), 0o600)
	directory := openTestDirectory(t, path)
	wantErr := errors.New("final record failed")

	session, err := InitializeSource(context.Background(), directory, identity, testInitializationOptions(), func(mode RecordMode, record Record) error {
		if mode == RecordModeLive {
			return wantErr
		}
		return nil
	})
	if err != nil {
		t.Fatalf("InitializeSource() error = %v", err)
	}
	t.Cleanup(func() { session.Close() })
	appendReport(t, reportPath, `{"n":2},`, identity.CreationTime.Add(2*time.Second))

	_, err = session.DrainSource(context.Background(), DrainOptions{
		Timeout:        10 * time.Millisecond,
		RescanInterval: time.Millisecond,
	})
	if !errors.Is(err, wantErr) {
		t.Fatalf("DrainSource() error = %v, want handler failure", err)
	}
}
