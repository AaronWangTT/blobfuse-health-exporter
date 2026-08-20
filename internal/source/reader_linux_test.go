//go:build linux

package source

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

func TestGenerationReaderRetainsPartialRecordAcrossAppend(t *testing.T) {
	initial := "[{\"n\":1},\n{\"n\":2},\n{\"n\":"
	reader, reportPath := openGenerationReader(t, initial)

	var values []int
	result, err := reader.drain(func(record Record) error {
		var value struct {
			Number int `json:"n"`
		}
		if err := record.Decode(&value); err != nil {
			return err
		}
		values = append(values, value.Number)
		return nil
	})
	if err != nil {
		t.Fatalf("drain() error = %v", err)
	}
	if result.records != 2 || result.closed {
		t.Fatalf("result = %#v, want two records and an open generation", result)
	}
	watermark := reader.watermark()
	if watermark.CommittedOffset <= 1 || watermark.CommittedOffset >= int64(len(initial)) {
		t.Fatalf("CommittedOffset = %d, want last complete boundary", watermark.CommittedOffset)
	}
	if !watermark.HasFingerprint {
		t.Fatal("HasFingerprint = false after complete records")
	}

	appendReport(t, reportPath, "3}]", testIdentity().CreationTime.Add(2*time.Second))
	result, err = reader.drain(func(record Record) error {
		var value struct {
			Number int `json:"n"`
		}
		if err := record.Decode(&value); err != nil {
			return err
		}
		values = append(values, value.Number)
		return nil
	})
	if err != nil {
		t.Fatalf("second drain() error = %v", err)
	}
	if result.records != 1 || !result.closed {
		t.Fatalf("second result = %#v, want one record and a closed generation", result)
	}
	if want := []int{1, 2, 3}; !reflect.DeepEqual(values, want) {
		t.Fatalf("values = %v, want %v", values, want)
	}
	if watermark := reader.watermark(); !watermark.Closed || watermark.CommittedOffset != int64(len(initial)+3) {
		t.Fatalf("watermark = %#v, want clean close at final size", watermark)
	}
}

func TestGenerationReaderRetriesRecordAfterApplyFailure(t *testing.T) {
	reader, _ := openGenerationReader(t, `[{"n":1},`)
	wantErr := errors.New("apply failed")
	var firstFingerprint [32]byte

	_, err := reader.drain(func(record Record) error {
		firstFingerprint = record.Fingerprint
		return wantErr
	})
	if !errors.Is(err, wantErr) {
		t.Fatalf("drain() error = %v, want apply failure", err)
	}
	if watermark := reader.watermark(); watermark.HasFingerprint {
		t.Fatalf("watermark = %#v, want no committed record", watermark)
	}

	result, err := reader.drain(func(record Record) error {
		if record.Fingerprint != firstFingerprint {
			return fmt.Errorf("retry fingerprint changed")
		}
		return nil
	})
	if err != nil {
		t.Fatalf("retry drain() error = %v", err)
	}
	if result.records != 1 || !reader.watermark().HasFingerprint {
		t.Fatalf("retry result = %#v, watermark = %#v", result, reader.watermark())
	}
}

func TestGenerationReaderDetectsTruncation(t *testing.T) {
	reader, reportPath := openGenerationReader(t, `[{"n":1},`)
	if _, err := reader.drain(func(Record) error { return nil }); err != nil {
		t.Fatalf("drain() error = %v", err)
	}
	if err := os.Truncate(reportPath, 0); err != nil {
		t.Fatalf("Truncate() error = %v", err)
	}

	if _, err := reader.drain(func(Record) error { return nil }); !errors.Is(err, ErrGenerationTruncated) {
		t.Fatalf("drain() error = %v, want ErrGenerationTruncated", err)
	}
}

func openGenerationReader(t *testing.T, contents string) (*generationReader, string) {
	t.Helper()
	path := secureTempDir(t)
	reportPath := filepath.Join(path, "monitor_1234.json")
	writeReport(t, reportPath, contents, testIdentity().CreationTime.Add(time.Second), 0o600)

	directory, err := OpenReportDirectory(path, false)
	if err != nil {
		t.Fatalf("OpenReportDirectory() error = %v", err)
	}
	t.Cleanup(func() { directory.Close() })
	discovered, err := directory.ScanReports(testIdentity())
	if err != nil {
		t.Fatalf("ScanReports() error = %v", err)
	}
	if len(discovered) != 1 {
		closeDiscovered(discovered)
		t.Fatalf("len(discovered) = %d, want 1", len(discovered))
	}

	reader, err := newGenerationReader(discovered[0], 1024)
	if err != nil {
		closeDiscovered(discovered)
		t.Fatalf("newGenerationReader() error = %v", err)
	}
	t.Cleanup(func() { reader.close() })
	return reader, reportPath
}

func appendReport(t *testing.T, path, contents string, modificationTime time.Time) {
	t.Helper()
	file, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatalf("OpenFile() error = %v", err)
	}
	if _, err := file.WriteString(contents); err != nil {
		file.Close()
		t.Fatalf("WriteString() error = %v", err)
	}
	if err := file.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	if err := os.Chtimes(path, modificationTime, modificationTime); err != nil {
		t.Fatalf("Chtimes() error = %v", err)
	}
}
