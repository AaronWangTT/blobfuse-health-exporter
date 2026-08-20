package metrics_test

import (
	"reflect"
	"testing"

	"github.com/AaronWangTT/blobfuse-health-exporter/internal/metrics"
	"github.com/AaronWangTT/blobfuse-health-exporter/internal/source"
)

var (
	readSeries = source.Series{
		Metric:    source.MetricStorageIO,
		Direction: source.DirectionRead,
	}
	writeSeries = source.Series{
		Metric:    source.MetricStorageIO,
		Direction: source.DirectionWrite,
	}
	openFilesSeries = source.Series{
		Metric:    source.MetricOpenFiles,
		Component: source.ComponentLibfuse,
	}
)

func TestStateBaselineIsIndependentPerCounterSeries(t *testing.T) {
	state := metrics.NewState()

	first := state.Apply(source.RecordModeBaseline, counterRecord(readSeries, 100))
	second := state.Apply(source.RecordModeBaseline, counterRecord(writeSeries, 50))
	if len(first.CounterIncrements) != 0 || len(second.CounterIncrements) != 0 {
		t.Fatalf("baseline emitted increments: first=%#v second=%#v", first, second)
	}

	want := map[source.Series]uint64{
		readSeries:  100,
		writeSeries: 50,
	}
	if got := state.CounterBaselines(); !reflect.DeepEqual(got, want) {
		t.Fatalf("CounterBaselines() = %#v, want %#v", got, want)
	}
}

func TestStateTranslatesLiveCounterDeltasAndResets(t *testing.T) {
	state := metrics.NewState()
	state.Apply(source.RecordModeBaseline, counterRecord(readSeries, 100))

	increased := state.Apply(source.RecordModeLive, counterRecord(readSeries, 130))
	if want := []metrics.CounterIncrement{{Series: readSeries, Delta: 30}}; !reflect.DeepEqual(increased.CounterIncrements, want) {
		t.Fatalf("increments = %#v, want %#v", increased.CounterIncrements, want)
	}

	equal := state.Apply(source.RecordModeLive, counterRecord(readSeries, 130))
	if len(equal.CounterIncrements) != 0 || len(equal.CounterResets) != 0 {
		t.Fatalf("equal value produced changes: %#v", equal)
	}

	reset := state.Apply(source.RecordModeLive, counterRecord(readSeries, 90))
	wantReset := []metrics.CounterReset{{Series: readSeries, Previous: 130, Current: 90}}
	if !reflect.DeepEqual(reset.CounterResets, wantReset) || len(reset.CounterIncrements) != 0 {
		t.Fatalf("reset result = %#v, want resets %#v", reset, wantReset)
	}

	afterReset := state.Apply(source.RecordModeLive, counterRecord(readSeries, 100))
	if want := []metrics.CounterIncrement{{Series: readSeries, Delta: 10}}; !reflect.DeepEqual(afterReset.CounterIncrements, want) {
		t.Fatalf("post-reset increments = %#v, want %#v", afterReset.CounterIncrements, want)
	}
}

func TestStateFirstCounterObservedLiveEstablishesBaseline(t *testing.T) {
	state := metrics.NewState()
	result := state.Apply(source.RecordModeLive, counterRecord(writeSeries, 500))

	if len(result.CounterIncrements) != 0 || len(result.CounterResets) != 0 {
		t.Fatalf("first live value produced changes: %#v", result)
	}
	if got := state.CounterBaselines()[writeSeries]; got != 500 {
		t.Fatalf("baseline = %d, want 500", got)
	}
}

func TestStateRetainsAndClearsSessionGauges(t *testing.T) {
	state := metrics.NewState()
	baseline := state.Apply(source.RecordModeBaseline, gaugeRecord(openFilesSeries, 2))
	if want := []metrics.GaugeUpdate{{Series: openFilesSeries, Value: 2}}; !reflect.DeepEqual(baseline.GaugeUpdates, want) {
		t.Fatalf("baseline gauge updates = %#v, want %#v", baseline.GaugeUpdates, want)
	}

	state.Apply(source.RecordModeLive, source.NormalizedRecord{})
	if got := state.Gauges()[openFilesSeries]; got != 2 {
		t.Fatalf("gauge after missing update = %v, want retained value 2", got)
	}
	state.Apply(source.RecordModeLive, gaugeRecord(openFilesSeries, 5))
	if got := state.Gauges()[openFilesSeries]; got != 5 {
		t.Fatalf("updated gauge = %v, want 5", got)
	}

	removed := state.ClearGauges()
	if len(removed) != 1 || removed[0] != openFilesSeries {
		t.Fatalf("ClearGauges() = %#v, want open-files series", removed)
	}
	if len(state.Gauges()) != 0 {
		t.Fatalf("Gauges() = %#v after clear", state.Gauges())
	}
}

func counterRecord(series source.Series, value uint64) source.NormalizedRecord {
	return source.NormalizedRecord{
		Counters: []source.CounterValue{{Series: series, Value: value}},
	}
}

func gaugeRecord(series source.Series, value float64) source.NormalizedRecord {
	return source.NormalizedRecord{
		Gauges: []source.GaugeValue{{Series: series, Value: value}},
	}
}
