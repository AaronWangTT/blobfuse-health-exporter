package source_test

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/AaronWangTT/blobfuse-health-exporter/internal/source"
)

func TestBlobFuse256FramingFixtures(t *testing.T) {
	tests := []struct {
		name     string
		records  int
		closed   bool
		needMore bool
	}{
		{name: "empty-active.json", needMore: true},
		{name: "trailing-comma.json", records: 1, needMore: true},
		{name: "partial-object.json", needMore: true},
		{name: "closed.json", records: 1, closed: true},
		{name: "escaped-path.json", records: 1, closed: true},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			data, err := os.ReadFile(filepath.Join("testdata", "blobfuse-2.5.6", "framing", test.name))
			if err != nil {
				t.Fatalf("ReadFile() error = %v", err)
			}
			decoder := newDecoder(t, 1024*1024)
			appendAtReadOffset(t, decoder, data)

			records := 0
			needMore := false
			for {
				record, err := decoder.Next()
				if errors.Is(err, source.ErrNeedMore) {
					needMore = true
					break
				}
				if errors.Is(err, source.ErrClosed) {
					break
				}
				if err != nil {
					t.Fatalf("Next() error = %v", err)
				}
				records++
				if err := decoder.Commit(record); err != nil {
					t.Fatalf("Commit() error = %v", err)
				}
			}

			if records != test.records || decoder.Closed() != test.closed || needMore != test.needMore {
				t.Fatalf(
					"records=%d closed=%t needMore=%t, want records=%d closed=%t needMore=%t",
					records,
					decoder.Closed(),
					needMore,
					test.records,
					test.closed,
					test.needMore,
				)
			}
		})
	}
}

func TestEscapedPathFixtureDoesNotSurviveNormalization(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("testdata", "blobfuse-2.5.6", "framing", "escaped-path.json"))
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	decoder := newDecoder(t, len(data))
	appendAtReadOffset(t, decoder, data)
	record, err := decoder.Next()
	if err != nil {
		t.Fatalf("Next() error = %v", err)
	}
	normalized, err := source.NormalizeRecord(record)
	if err != nil {
		t.Fatalf("NormalizeRecord() error = %v", err)
	}
	dump := strings.ToLower(fmt.Sprintf("%#v", normalized))
	for _, prohibited := range []string{
		"private/quote",
		"snow-",
		"private/source",
		"private/destination",
		"newline\\nfile",
	} {
		if strings.Contains(dump, prohibited) {
			t.Fatalf("normalized result retained path sentinel %q", prohibited)
		}
	}
}
