//go:build linux

package source

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

func TestDrainSourceReportsVersionedOpenTailFixtures(t *testing.T) {
	tests := []struct {
		name    string
		fixture string
	}{
		{name: "graceful_empty", fixture: "empty-active.json"},
		{name: "graceful_trailing_comma", fixture: "trailing-comma.json"},
		{name: "abrupt_partial_object", fixture: "partial-object.json"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			contents, err := os.ReadFile(filepath.Join(
				"testdata",
				"blobfuse-2.5.6",
				"framing",
				test.fixture,
			))
			if err != nil {
				t.Fatalf("ReadFile() error = %v", err)
			}
			path := secureTempDir(t)
			identity := testIdentity()
			writeReport(
				t,
				filepath.Join(path, "monitor_1234.json"),
				string(contents),
				identity.CreationTime.Add(time.Second),
				0o600,
			)
			directory := openTestDirectory(t, path)
			session, err := InitializeSource(
				context.Background(),
				directory,
				identity,
				testInitializationOptions(),
				func(RecordMode, Record) error { return nil },
			)
			if err != nil {
				t.Fatalf("InitializeSource() error = %v", err)
			}
			t.Cleanup(func() { session.Close() })

			result, err := session.DrainSource(context.Background(), DrainOptions{
				Timeout:        time.Millisecond,
				RescanInterval: time.Millisecond,
			})
			if err != nil {
				t.Fatalf("DrainSource() error = %v", err)
			}
			if len(result.UncleanGenerations) != 1 || result.UncleanGenerations[0].Closed {
				t.Fatalf("unclean generations = %#v, want one open tail", result.UncleanGenerations)
			}
		})
	}
}

func TestDrainSourceConsumesObservationQueuedDuringFinalDrain(t *testing.T) {
	path := secureTempDir(t)
	identity := testIdentity()
	reportPath := filepath.Join(path, "monitor_1234.json")
	writeReport(t, reportPath, `[{"n":1},`, identity.CreationTime.Add(time.Second), 0o600)
	directory := openTestDirectory(t, path)

	var liveValues []int
	session, err := InitializeSource(
		context.Background(),
		directory,
		identity,
		testInitializationOptions(),
		func(mode RecordMode, record Record) error {
			if mode != RecordModeLive {
				return nil
			}
			value := decodeTestNumber(t, record)
			liveValues = append(liveValues, value)
			if value == 2 {
				appendReport(t, reportPath, `{"n":3}]`, identity.CreationTime.Add(3*time.Second))
			}
			return nil
		},
	)
	if err != nil {
		t.Fatalf("InitializeSource() error = %v", err)
	}
	t.Cleanup(func() { session.Close() })
	appendReport(t, reportPath, `{"n":2},`, identity.CreationTime.Add(2*time.Second))

	result, err := session.DrainSource(context.Background(), DrainOptions{
		Timeout:        time.Millisecond,
		RescanInterval: time.Millisecond,
	})
	if err != nil {
		t.Fatalf("DrainSource() error = %v", err)
	}
	if result.Records != 2 || len(result.UncleanGenerations) != 0 {
		t.Fatalf("result = %#v, want two final records and a clean close", result)
	}
	if want := []int{2, 3}; !reflect.DeepEqual(liveValues, want) {
		t.Fatalf("live values = %v, want %v", liveValues, want)
	}
}
