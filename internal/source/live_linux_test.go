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

func TestSourceSessionReconcileProcessesAppendAfterCutoverAsLive(t *testing.T) {
	path := secureTempDir(t)
	identity := testIdentity()
	reportPath := filepath.Join(path, "monitor_1234.json")
	writeReport(t, reportPath, `[{"n":1},`, identity.CreationTime.Add(time.Second), 0o600)
	directory := openTestDirectory(t, path)

	var modes []RecordMode
	var values []int
	session, err := InitializeSource(context.Background(), directory, identity, testInitializationOptions(), func(mode RecordMode, record Record) error {
		modes = append(modes, mode)
		values = append(values, decodeTestNumber(t, record))
		return nil
	})
	if err != nil {
		t.Fatalf("InitializeSource() error = %v", err)
	}
	t.Cleanup(func() { session.Close() })
	cutover := session.Cutover()

	appendReport(t, reportPath, `{"n":2},`, identity.CreationTime.Add(2*time.Second))
	result, err := session.Reconcile(context.Background())
	if err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}
	if result.Records != 1 || result.NewGenerations != 0 {
		t.Fatalf("result = %#v, want one record on the known generation", result)
	}
	if want := []int{1, 2}; !reflect.DeepEqual(values, want) {
		t.Fatalf("values = %v, want %v", values, want)
	}
	if want := []RecordMode{RecordModeBaseline, RecordModeLive}; !reflect.DeepEqual(modes, want) {
		t.Fatalf("modes = %v, want %v", modes, want)
	}
	if !reflect.DeepEqual(session.Cutover(), cutover) {
		t.Fatal("Reconcile() changed the immutable baseline cutover")
	}
	current := session.CurrentWatermarks()
	if len(current) != 1 || current[0].CommittedOffset <= cutover[0].CommittedOffset {
		t.Fatalf("current watermarks = %#v, want advancement beyond %#v", current, cutover)
	}
}

func TestSourceSessionReconcileProcessesRotationOldestFirst(t *testing.T) {
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
	writeReport(t, currentPath, `[{"n":3},`, identity.CreationTime.Add(3*time.Second), 0o600)

	result, err := session.Reconcile(context.Background())
	if err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}
	if result.Records != 2 || result.NewGenerations != 1 {
		t.Fatalf("result = %#v, want two records and one new generation", result)
	}
	if want := []int{2, 3}; !reflect.DeepEqual(liveValues, want) {
		t.Fatalf("live values = %v, want oldest generation first: %v", liveValues, want)
	}
}

func TestSourceSessionReconcileRecoversAfterNotificationWasDrained(t *testing.T) {
	path := secureTempDir(t)
	identity := testIdentity()
	currentPath := filepath.Join(path, "monitor_1234.json")
	writeReport(t, currentPath, `[{"n":1}]`, identity.CreationTime.Add(time.Second), 0o600)
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

	writeReport(t, filepath.Join(path, "monitor_1234_1.json"), `[{"n":2}]`, identity.CreationTime.Add(2*time.Second), 0o600)
	if _, err := session.watcher.Drain(); err != nil {
		t.Fatalf("pre-test Drain() error = %v", err)
	}

	result, err := session.Reconcile(context.Background())
	if err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}
	if result.NewGenerations != 1 || !reflect.DeepEqual(liveValues, []int{2}) {
		t.Fatalf("result = %#v, live values = %v; want rescan recovery", result, liveValues)
	}
}

func TestSourceSessionReconcileRetriesAfterHandlerFailure(t *testing.T) {
	path := secureTempDir(t)
	identity := testIdentity()
	reportPath := filepath.Join(path, "monitor_1234.json")
	writeReport(t, reportPath, `[{"n":1},`, identity.CreationTime.Add(time.Second), 0o600)
	directory := openTestDirectory(t, path)
	wantErr := errors.New("live apply failed")
	failLive := true
	liveCalls := 0

	session, err := InitializeSource(context.Background(), directory, identity, testInitializationOptions(), func(mode RecordMode, record Record) error {
		if mode == RecordModeLive {
			liveCalls++
			if failLive {
				return wantErr
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("InitializeSource() error = %v", err)
	}
	t.Cleanup(func() { session.Close() })
	appendReport(t, reportPath, `{"n":2},`, identity.CreationTime.Add(2*time.Second))

	if _, err := session.Reconcile(context.Background()); !errors.Is(err, wantErr) {
		t.Fatalf("first Reconcile() error = %v, want handler failure", err)
	}
	failLive = false
	if _, err := session.Reconcile(context.Background()); err != nil {
		t.Fatalf("retry Reconcile() error = %v", err)
	}
	if liveCalls != 2 {
		t.Fatalf("live handler calls = %d, want the same record retried", liveCalls)
	}
}
