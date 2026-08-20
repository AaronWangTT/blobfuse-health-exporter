package metrics

import (
	"context"
	"fmt"
	"sync"

	"github.com/AaronWangTT/blobfuse-health-exporter/internal/source"
	"go.opentelemetry.io/otel/attribute"
	otelmetric "go.opentelemetry.io/otel/metric"
)

// OTelRecorder records translated counter additions and observes current gauge
// state at collection time so source-session gauge removal is visible.
type OTelRecorder struct {
	state                *State
	counters             map[source.Metric]otelmetric.Int64Counter
	gauges               map[source.Metric]otelmetric.Float64ObservableGauge
	upDownCounters       map[source.Metric]otelmetric.Float64ObservableUpDownCounter
	registration         otelmetric.Registration
	mutex                sync.RWMutex
	droppedInvalidSeries uint64
}

func NewOTelRecorder(meter otelmetric.Meter, state *State) (*OTelRecorder, error) {
	if meter == nil {
		return nil, fmt.Errorf("OpenTelemetry meter is required")
	}
	if state == nil {
		return nil, fmt.Errorf("metric state is required")
	}
	recorder := &OTelRecorder{
		state:          state,
		counters:       make(map[source.Metric]otelmetric.Int64Counter),
		gauges:         make(map[source.Metric]otelmetric.Float64ObservableGauge),
		upDownCounters: make(map[source.Metric]otelmetric.Float64ObservableUpDownCounter),
	}

	for _, series := range representativeSeries() {
		descriptor, _, err := DescribeSeries(series)
		if err != nil {
			return nil, err
		}
		switch descriptor.Kind {
		case InstrumentCounter:
			if _, exists := recorder.counters[series.Metric]; exists {
				continue
			}
			instrument, err := meter.Int64Counter(
				descriptor.Name,
				otelmetric.WithDescription(descriptor.Description),
				otelmetric.WithUnit(descriptor.Unit),
			)
			if err != nil {
				return nil, err
			}
			recorder.counters[series.Metric] = instrument

		case InstrumentGauge:
			instrument, err := meter.Float64ObservableGauge(
				descriptor.Name,
				otelmetric.WithDescription(descriptor.Description),
				otelmetric.WithUnit(descriptor.Unit),
			)
			if err != nil {
				return nil, err
			}
			recorder.gauges[series.Metric] = instrument

		case InstrumentObservableUpDownCounter:
			instrument, err := meter.Float64ObservableUpDownCounter(
				descriptor.Name,
				otelmetric.WithDescription(descriptor.Description),
				otelmetric.WithUnit(descriptor.Unit),
			)
			if err != nil {
				return nil, err
			}
			recorder.upDownCounters[series.Metric] = instrument
		}
	}

	observables := make([]otelmetric.Observable, 0, len(recorder.gauges)+len(recorder.upDownCounters))
	for _, instrument := range recorder.gauges {
		observables = append(observables, instrument)
	}
	for _, instrument := range recorder.upDownCounters {
		observables = append(observables, instrument)
	}
	registration, err := meter.RegisterCallback(recorder.observeGauges, observables...)
	if err != nil {
		return nil, err
	}
	recorder.registration = registration
	return recorder, nil
}

// Record is a no-fail Processor observer. Invalid series are counted and
// dropped rather than introducing a failure after metric state was applied.
func (recorder *OTelRecorder) Record(processed ProcessedRecord) {
	for _, increment := range processed.Metrics.CounterIncrements {
		instrument, found := recorder.counters[increment.Series.Metric]
		attributes, ok := metricAttributeSet(increment.Series)
		if !found || !ok {
			recorder.recordInvalidSeries()
			continue
		}
		instrument.Add(
			context.Background(),
			int64(increment.Delta),
			otelmetric.WithAttributeSet(attributes),
		)
	}
}

func (recorder *OTelRecorder) DroppedInvalidSeries() uint64 {
	recorder.mutex.RLock()
	defer recorder.mutex.RUnlock()
	return recorder.droppedInvalidSeries
}

func (recorder *OTelRecorder) Close() error {
	if recorder == nil || recorder.registration == nil {
		return nil
	}
	return recorder.registration.Unregister()
}

func (recorder *OTelRecorder) observeGauges(_ context.Context, observer otelmetric.Observer) error {
	for series, value := range recorder.state.Gauges() {
		attributes, ok := metricAttributeSet(series)
		if !ok {
			recorder.recordInvalidSeries()
			continue
		}
		option := otelmetric.WithAttributeSet(attributes)
		if instrument, found := recorder.gauges[series.Metric]; found {
			observer.ObserveFloat64(instrument, value, option)
			continue
		}
		if instrument, found := recorder.upDownCounters[series.Metric]; found {
			observer.ObserveFloat64(instrument, value, option)
			continue
		}
		recorder.recordInvalidSeries()
	}
	return nil
}

func (recorder *OTelRecorder) recordInvalidSeries() {
	recorder.mutex.Lock()
	defer recorder.mutex.Unlock()
	recorder.droppedInvalidSeries++
}

func metricAttributeSet(series source.Series) (attribute.Set, bool) {
	_, attributes, err := DescribeSeries(series)
	if err != nil {
		return attribute.Set{}, false
	}
	keyValues := make([]attribute.KeyValue, 0, len(attributes))
	for _, metricAttribute := range attributes {
		keyValues = append(keyValues, attribute.String(metricAttribute.Key, metricAttribute.Value))
	}
	return attribute.NewSet(keyValues...), true
}

func representativeSeries() []source.Series {
	return []source.Series{
		{Metric: source.MetricStorageIO, Direction: source.DirectionRead},
		{Metric: source.MetricFSOperations, Operation: source.OperationCreateFile},
		{Metric: source.MetricOpenFiles, Component: source.ComponentLibfuse},
		{Metric: source.MetricCacheDownloads},
		{Metric: source.MetricCacheHits},
		{Metric: source.MetricCacheUsage},
		{Metric: source.MetricCacheUtilization},
		{Metric: source.MetricMemoryVirtual},
	}
}
