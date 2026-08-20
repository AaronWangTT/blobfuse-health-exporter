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

func TestSourceObserverReportsLiveRotationAndUncleanClose(t *testing.T) {
	path := secureTempDir(t)
	identity := testIdentity()
	currentPath := filepath.Join(path, "monitor_1234.json")
	retainedPath := filepath.Join(path, "monitor_1234_1.json")
	writeReport(t, currentPath, `[{"n":1},`, identity.CreationTime.Add(time.Second), 0o600)
	directory := openTestDirectory(t, path)
	session, err := InitializeSource(context.Background(), directory, identity, testInitializationOptions(), func(RecordMode, Record) error {
		return nil
	})
	if err != nil {
		t.Fatalf("InitializeSource() error = %v", err)
	}
	t.Cleanup(func() { session.Close() })

	var events []SourceEvent
	if err := session.AttachObserver(func(event SourceEvent) {
		events = append(events, event)
	}); err != nil {
		t.Fatalf("AttachObserver() error = %v", err)
	}
	if err := os.Rename(currentPath, retainedPath); err != nil {
		t.Fatalf("Rename() error = %v", err)
	}
	appendReport(t, retainedPath, `{"n":2}]`, identity.CreationTime.Add(2*time.Second))
	writeReport(t, currentPath, `[{"n":3},`, identity.CreationTime.Add(3*time.Second), 0o600)

	if _, err := session.Reconcile(context.Background()); err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}
	if _, err := session.DrainSource(context.Background(), DrainOptions{
		Timeout:        time.Millisecond,
		RescanInterval: time.Millisecond,
	}); err != nil {
		t.Fatalf("DrainSource() error = %v", err)
	}

	want := []SourceEvent{
		{Kind: SourceEventRotations, Count: 1},
		{Kind: SourceEventDiscontinuity, Count: 1, Reason: SourceDiscontinuityUncleanClose},
	}
	if !reflect.DeepEqual(events, want) {
		t.Fatalf("events = %#v, want %#v", events, want)
	}
}

func TestDiscontinuityForErrorUsesFixedAllowlist(t *testing.T) {
	tests := []struct {
		err    error
		reason SourceDiscontinuity
		ok     bool
	}{
		{err: ErrGenerationTruncated, reason: SourceDiscontinuityGenerationTruncated, ok: true},
		{err: ErrRecordTooLarge, reason: SourceDiscontinuityOversizeRecord, ok: true},
		{err: ErrStaleGeneration, reason: SourceDiscontinuityStaleGeneration, ok: true},
		{err: errors.New("private path"), ok: false},
	}
	for _, test := range tests {
		reason, ok := discontinuityForError(test.err)
		if reason != test.reason || ok != test.ok {
			t.Fatalf("discontinuityForError(%v) = (%d, %t), want (%d, %t)", test.err, reason, ok, test.reason, test.ok)
		}
	}
}
