package source_test

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/AaronWangTT/blobfuse-health-exporter/internal/metrics"
	"github.com/AaronWangTT/blobfuse-health-exporter/internal/source"
)

func TestBlobFuse256OmittedFieldSequenceRetainsKnownStateWithoutSynthesizingUnknowns(t *testing.T) {
	data, err := os.ReadFile(filepath.Join(
		"testdata",
		"blobfuse-2.5.6",
		"sequences",
		"omitted-fields.json",
	))
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	decoder := newDecoder(t, len(data))
	appendAtReadOffset(t, decoder, data)

	var records []source.NormalizedRecord
	for {
		record, err := decoder.Next()
		if errors.Is(err, source.ErrClosed) {
			break
		}
		if err != nil {
			t.Fatalf("Next() error = %v", err)
		}
		normalized, err := source.NormalizeRecord(record)
		if err != nil {
			t.Fatalf("NormalizeRecord() error = %v", err)
		}
		records = append(records, normalized)
		if err := decoder.Commit(record); err != nil {
			t.Fatalf("Commit() error = %v", err)
		}
	}
	if len(records) != 2 {
		t.Fatalf("records = %d, want 2", len(records))
	}
	if !records[0].HasTimestamp || !records[1].HasTimestamp || !records[0].Timestamp.Equal(records[1].Timestamp) {
		t.Fatalf("timestamps = %v and %v, want the same source timestamp", records[0].Timestamp, records[1].Timestamp)
	}

	readSeries := source.Series{Metric: source.MetricStorageIO, Direction: source.DirectionRead}
	writeSeries := source.Series{Metric: source.MetricStorageIO, Direction: source.DirectionWrite}
	openFilesSeries := source.Series{Metric: source.MetricOpenFiles, Component: source.ComponentLibfuse}
	memorySeries := source.Series{Metric: source.MetricMemoryVirtual}
	state := metrics.NewState()
	state.Apply(source.RecordModeBaseline, records[0])
	live := state.Apply(source.RecordModeLive, records[1])

	if len(live.CounterIncrements) != 1 || live.CounterIncrements[0].Series != readSeries || live.CounterIncrements[0].Delta != 30 {
		t.Fatalf("live increments = %#v, want one read increment of 30", live.CounterIncrements)
	}
	gauges := state.Gauges()
	if gauges[openFilesSeries] != 3 || gauges[memorySeries] != 100*(1<<20) {
		t.Fatalf("retained gauges = %#v, want open files 3 and memory 100 MiB", gauges)
	}
	baselines := state.CounterBaselines()
	if baselines[readSeries] != 130 {
		t.Fatalf("read baseline = %d, want 130", baselines[readSeries])
	}
	if _, found := baselines[writeSeries]; found {
		t.Fatalf("never-observed write series was synthesized: %#v", baselines)
	}
}
