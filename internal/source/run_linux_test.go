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

func TestSourceSessionRunProcessesEventsThenDrainsEndedSource(t *testing.T) {
	path := secureTempDir(t)
	identity := testIdentity()
	reportPath := filepath.Join(path, "monitor_1234.json")
	writeReport(t, reportPath, `[{"n":1},`, identity.CreationTime.Add(time.Second), 0o600)
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
	appendReport(t, reportPath, `{"n":2},`, identity.CreationTime.Add(2*time.Second))

	lookups := 0
	result, err := session.run(context.Background(), testRunOptions(), func(int) (ProcessIdentity, error) {
		lookups++
		if lookups == 1 {
			return identity, nil
		}
		return ProcessIdentity{}, os.ErrNotExist
	})
	if err != nil {
		t.Fatalf("run() error = %v", err)
	}
	if !errors.Is(result.SourceEnd, ErrSourceSessionEnded) || !errors.Is(result.SourceEnd, os.ErrNotExist) {
		t.Fatalf("SourceEnd = %v, want unavailable source identity", result.SourceEnd)
	}
	if result.Reconciliations != 1 || result.Live.Records != 1 {
		t.Fatalf("result = %#v, want one live reconciliation and record", result)
	}
	if !reflect.DeepEqual(liveValues, []int{2}) {
		t.Fatalf("live values = %v, want [2]", liveValues)
	}
	if len(result.Drain.UncleanGenerations) != 1 {
		t.Fatalf("drain = %#v, want the active generation reported unclean", result.Drain)
	}
}

func TestSourceSessionRunDrainsChangedIdentityWithoutReattaching(t *testing.T) {
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

	result, err := session.run(context.Background(), testRunOptions(), func(int) (ProcessIdentity, error) {
		changed := identity
		changed.StartTicks++
		return changed, nil
	})
	if err != nil {
		t.Fatalf("run() error = %v", err)
	}
	if !errors.Is(result.SourceEnd, ErrSourceSessionEnded) {
		t.Fatalf("SourceEnd = %v, want ErrSourceSessionEnded", result.SourceEnd)
	}
	if result.Reconciliations != 0 {
		t.Fatalf("Reconciliations = %d, want no attachment to replacement", result.Reconciliations)
	}
}

func TestSourceSessionRunSkipsDrainOnExternalCancellation(t *testing.T) {
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

	result, err := session.run(ctx, testRunOptions(), func(int) (ProcessIdentity, error) {
		t.Fatal("identity lookup called after external cancellation")
		return ProcessIdentity{}, nil
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("run() error = %v, want context.Canceled", err)
	}
	if result.SourceEnd != nil || len(result.Drain.UncleanGenerations) != 0 {
		t.Fatalf("result = %#v, want no source drain", result)
	}
}

func testRunOptions() RunOptions {
	return RunOptions{
		RescanInterval:     time.Millisecond,
		SourceDrainTimeout: 5 * time.Millisecond,
	}
}
