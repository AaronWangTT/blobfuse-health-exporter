package metrics_test

import (
	"reflect"
	"testing"

	"github.com/AaronWangTT/blobfuse-health-exporter/internal/metrics"
	"github.com/AaronWangTT/blobfuse-health-exporter/internal/source"
)

func TestProcessorAppliesBaselineBeforeLiveDelta(t *testing.T) {
	state := metrics.NewState()
	var observed []metrics.ProcessedRecord
	processor, err := metrics.NewProcessor(state, func(record metrics.ProcessedRecord) {
		observed = append(observed, record)
	})
	if err != nil {
		t.Fatalf("NewProcessor() error = %v", err)
	}

	if err := processor.Process(source.RecordModeBaseline, source.Record{Raw: []byte(`{
		"BlobfuseStats":[{"componentName":"azstorage","value":{"Bytes Downloaded":100}}]
	}`)}); err != nil {
		t.Fatalf("baseline Process() error = %v", err)
	}
	if err := processor.Handler()(source.RecordModeLive, source.Record{Raw: []byte(`{
		"BlobfuseStats":[{"componentName":"azstorage","value":{"Bytes Downloaded":130}}]
	}`)}); err != nil {
		t.Fatalf("live Handler() error = %v", err)
	}

	if len(observed) != 2 {
		t.Fatalf("observed records = %d, want 2", len(observed))
	}
	if len(observed[0].Metrics.CounterIncrements) != 0 {
		t.Fatalf("baseline increments = %#v, want none", observed[0].Metrics.CounterIncrements)
	}
	wantIncrement := []metrics.CounterIncrement{{Series: readSeries, Delta: 30}}
	if !reflect.DeepEqual(observed[1].Metrics.CounterIncrements, wantIncrement) {
		t.Fatalf("live increments = %#v, want %#v", observed[1].Metrics.CounterIncrements, wantIncrement)
	}
	if stats := processor.Stats(); stats.AcceptedRecords != 2 || stats.InvalidRecords != 0 {
		t.Fatalf("Stats() = %#v, want two accepted records", stats)
	}
}

func TestProcessorClassifiesIgnoredAndInvalidRecords(t *testing.T) {
	processor, err := metrics.NewProcessor(metrics.NewState(), nil)
	if err != nil {
		t.Fatalf("NewProcessor() error = %v", err)
	}

	if err := processor.Process(source.RecordModeLive, source.Record{Raw: []byte(`{"UnknownTopLevel":true}`)}); err != nil {
		t.Fatalf("ignored Process() error = %v", err)
	}
	if err := processor.Process(source.RecordModeLive, source.Record{Raw: []byte(`{"Timestamp":123}`)}); err != nil {
		t.Fatalf("invalid Process() error = %v", err)
	}

	stats := processor.Stats()
	if stats.IgnoredRecords != 1 || stats.InvalidRecords != 1 || stats.IgnoredValues != 1 || stats.InvalidValues != 1 {
		t.Fatalf("Stats() = %#v, want one ignored and one invalid record", stats)
	}
}

func TestProcessorObserverSeesInvalidNormalizationBeforeError(t *testing.T) {
	var observed []metrics.ProcessedRecord
	processor, err := metrics.NewProcessor(metrics.NewState(), func(record metrics.ProcessedRecord) {
		observed = append(observed, record)
	})
	if err != nil {
		t.Fatalf("NewProcessor() error = %v", err)
	}

	if err := processor.Process(source.RecordModeLive, source.Record{Raw: []byte(`[]`)}); err == nil {
		t.Fatal("Process() error = nil, want non-object rejection")
	}
	if len(observed) != 1 || observed[0].Outcome != metrics.RecordInvalid || observed[0].InvalidValues != 1 {
		t.Fatalf("observed = %#v, want one invalid outcome", observed)
	}
	if stats := processor.Stats(); stats.InvalidRecords != 1 || stats.InvalidValues != 1 {
		t.Fatalf("Stats() = %#v, want one invalid record and value", stats)
	}
}

func TestProcessorCountsResetWithoutEmittingIncrement(t *testing.T) {
	state := metrics.NewState()
	var last metrics.ProcessedRecord
	processor, err := metrics.NewProcessor(state, func(record metrics.ProcessedRecord) {
		last = record
	})
	if err != nil {
		t.Fatalf("NewProcessor() error = %v", err)
	}
	processor.Process(source.RecordModeBaseline, source.Record{Raw: []byte(`{
		"BlobfuseStats":[{"componentName":"azstorage","value":{"Bytes Downloaded":100}}]
	}`)})
	processor.Process(source.RecordModeLive, source.Record{Raw: []byte(`{
		"BlobfuseStats":[{"componentName":"azstorage","value":{"Bytes Downloaded":90}}]
	}`)})

	if len(last.Metrics.CounterResets) != 1 || len(last.Metrics.CounterIncrements) != 0 {
		t.Fatalf("processed reset = %#v", last)
	}
	if stats := processor.Stats(); stats.CounterResets != 1 {
		t.Fatalf("CounterResets = %d, want 1", stats.CounterResets)
	}
}

func TestProcessorObserverSeesAppliedGaugeState(t *testing.T) {
	state := metrics.NewState()
	processor, err := metrics.NewProcessor(state, func(record metrics.ProcessedRecord) {
		if record.Outcome != metrics.RecordAccepted {
			t.Fatalf("Outcome = %d, want RecordAccepted", record.Outcome)
		}
		if got := state.Gauges()[openFilesSeries]; got != 4 {
			t.Fatalf("observer gauge = %v, want applied value 4", got)
		}
	})
	if err != nil {
		t.Fatalf("NewProcessor() error = %v", err)
	}

	if err := processor.Process(source.RecordModeBaseline, source.Record{Raw: []byte(`{
		"BlobfuseStats":[{"componentName":"libfuse","value":{"OpenFileHandles":4}}]
	}`)}); err != nil {
		t.Fatalf("Process() error = %v", err)
	}
}

func TestProcessorAttachesObserverAfterBaselineWithoutReplay(t *testing.T) {
	state := metrics.NewState()
	processor, err := metrics.NewProcessor(state, nil)
	if err != nil {
		t.Fatalf("NewProcessor() error = %v", err)
	}
	if err := processor.Process(source.RecordModeBaseline, source.Record{Raw: []byte(`{
		"BlobfuseStats":[{"componentName":"azstorage","value":{"Bytes Downloaded":100}}]
	}`)}); err != nil {
		t.Fatalf("baseline Process() error = %v", err)
	}

	var observed []metrics.ProcessedRecord
	if err := processor.AttachObserver(func(record metrics.ProcessedRecord) {
		observed = append(observed, record)
	}); err != nil {
		t.Fatalf("AttachObserver() error = %v", err)
	}
	if len(observed) != 0 {
		t.Fatalf("AttachObserver() replayed %d baseline records", len(observed))
	}
	if err := processor.Process(source.RecordModeLive, source.Record{Raw: []byte(`{
		"BlobfuseStats":[{"componentName":"azstorage","value":{"Bytes Downloaded":130}}]
	}`)}); err != nil {
		t.Fatalf("live Process() error = %v", err)
	}
	if len(observed) != 1 || len(observed[0].Metrics.CounterIncrements) != 1 || observed[0].Metrics.CounterIncrements[0].Delta != 30 {
		t.Fatalf("observed = %#v, want one live delta of 30", observed)
	}
	if err := processor.AttachObserver(func(metrics.ProcessedRecord) {}); err == nil {
		t.Fatal("second AttachObserver() error = nil")
	}
}
