//go:build linux

package source

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestSourceSessionReportsOversizeRecordDiscontinuity(t *testing.T) {
	path := secureTempDir(t)
	identity := testIdentity()
	reportPath := filepath.Join(path, "monitor_1234.json")
	writeReport(t, reportPath, `[{"n":1},`, identity.CreationTime.Add(time.Second), 0o600)
	directory := openTestDirectory(t, path)
	options := testInitializationOptions()
	options.MaxRecordBytes = 1024
	session, err := InitializeSource(context.Background(), directory, identity, options, func(RecordMode, Record) error {
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
	appendReport(
		t,
		reportPath,
		fmt.Sprintf(`{"padding":%q},`, strings.Repeat("x", 2048)),
		identity.CreationTime.Add(2*time.Second),
	)

	_, err = session.Reconcile(context.Background())
	if !errors.Is(err, ErrRecordTooLarge) {
		t.Fatalf("Reconcile() error = %v, want ErrRecordTooLarge", err)
	}
	want := []SourceEvent{{
		Kind:   SourceEventDiscontinuity,
		Count:  1,
		Reason: SourceDiscontinuityOversizeRecord,
	}}
	if !reflect.DeepEqual(events, want) {
		t.Fatalf("events = %#v, want %#v", events, want)
	}
}
