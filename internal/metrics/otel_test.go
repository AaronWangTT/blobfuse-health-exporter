package metrics_test

import (
	"context"
	"testing"

	"github.com/AaronWangTT/blobfuse-health-exporter/internal/metrics"
	"github.com/AaronWangTT/blobfuse-health-exporter/internal/source"
	"go.opentelemetry.io/otel/attribute"
	otelmetric "go.opentelemetry.io/otel/metric"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
)

func TestOTelRecorderExportsCumulativeCounterAndCurrentGauges(t *testing.T) {
	reader := sdkmetric.NewManualReader(
		sdkmetric.WithTemporalitySelector(sdkmetric.CumulativeTemporalitySelector),
	)
	provider := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	t.Cleanup(func() { provider.Shutdown(context.Background()) })
	state := metrics.NewState()
	recorder, err := metrics.NewOTelRecorder(
		provider.Meter("blobfuse-health-exporter", otelmetric.WithInstrumentationVersion("test")),
		state,
	)
	if err != nil {
		t.Fatalf("NewOTelRecorder() error = %v", err)
	}
	t.Cleanup(func() { recorder.Close() })

	recorder.Record(metrics.ProcessedRecord{Metrics: state.Apply(
		source.RecordModeBaseline,
		otelRecord(100, 2, 1024),
	)})
	baseline := collectMetrics(t, reader)
	if findMetric(baseline, "azure.blobfuse.storage.io") != nil {
		t.Fatal("baseline-only counter produced a metric point")
	}
	assertGaugeValue(t, baseline, "azure.blobfuse.file.open", 2)
	assertNonMonotonicSumValue(t, baseline, "process.memory.virtual", 1024)

	recorder.Record(metrics.ProcessedRecord{Metrics: state.Apply(
		source.RecordModeLive,
		otelRecord(130, 3, 2048),
	)})
	firstLive := collectMetrics(t, reader)
	assertCumulativeCounter(t, firstLive, "azure.blobfuse.storage.io", 30)
	assertGaugeValue(t, firstLive, "azure.blobfuse.file.open", 3)
	assertNonMonotonicSumValue(t, firstLive, "process.memory.virtual", 2048)
	assertStringAttribute(t, firstLive, "azure.blobfuse.storage.io", metrics.AttributeIODirection, "read")

	recorder.Record(metrics.ProcessedRecord{Metrics: state.Apply(
		source.RecordModeLive,
		otelRecord(150, 3, 2048),
	)})
	secondLive := collectMetrics(t, reader)
	assertCumulativeCounter(t, secondLive, "azure.blobfuse.storage.io", 50)

	state.ClearGauges()
	cleared := collectMetrics(t, reader)
	assertNoDataPoints(t, cleared, "azure.blobfuse.file.open")
	assertNoDataPoints(t, cleared, "process.memory.virtual")
	if recorder.DroppedInvalidSeries() != 0 {
		t.Fatalf("DroppedInvalidSeries() = %d, want 0", recorder.DroppedInvalidSeries())
	}
}

func otelRecord(counter uint64, openFiles, memory float64) source.NormalizedRecord {
	return source.NormalizedRecord{
		Counters: []source.CounterValue{{Series: readSeries, Value: counter}},
		Gauges: []source.GaugeValue{
			{Series: openFilesSeries, Value: openFiles},
			{Series: source.Series{Metric: source.MetricMemoryVirtual}, Value: memory},
		},
	}
}

func collectMetrics(t *testing.T, reader *sdkmetric.ManualReader) metricdata.ResourceMetrics {
	t.Helper()
	var result metricdata.ResourceMetrics
	if err := reader.Collect(context.Background(), &result); err != nil {
		t.Fatalf("Collect() error = %v", err)
	}
	return result
}

func findMetric(resourceMetrics metricdata.ResourceMetrics, name string) *metricdata.Metrics {
	for _, scopeMetrics := range resourceMetrics.ScopeMetrics {
		for index := range scopeMetrics.Metrics {
			if scopeMetrics.Metrics[index].Name == name {
				return &scopeMetrics.Metrics[index]
			}
		}
	}
	return nil
}

func assertCumulativeCounter(t *testing.T, resourceMetrics metricdata.ResourceMetrics, name string, want int64) {
	t.Helper()
	metric := findMetric(resourceMetrics, name)
	if metric == nil {
		t.Fatalf("metric %q was not collected", name)
	}
	sum, ok := metric.Data.(metricdata.Sum[int64])
	if !ok {
		t.Fatalf("metric %q data type = %T, want int64 sum", name, metric.Data)
	}
	if sum.Temporality != metricdata.CumulativeTemporality || !sum.IsMonotonic {
		t.Fatalf("metric %q sum = %#v, want cumulative monotonic", name, sum)
	}
	if len(sum.DataPoints) != 1 || sum.DataPoints[0].Value != want {
		t.Fatalf("metric %q points = %#v, want value %d", name, sum.DataPoints, want)
	}
}

func assertGaugeValue(t *testing.T, resourceMetrics metricdata.ResourceMetrics, name string, want float64) {
	t.Helper()
	metric := findMetric(resourceMetrics, name)
	if metric == nil {
		t.Fatalf("metric %q was not collected", name)
	}
	gauge, ok := metric.Data.(metricdata.Gauge[float64])
	if !ok {
		t.Fatalf("metric %q data type = %T, want float64 gauge", name, metric.Data)
	}
	if len(gauge.DataPoints) != 1 || gauge.DataPoints[0].Value != want {
		t.Fatalf("metric %q points = %#v, want value %v", name, gauge.DataPoints, want)
	}
}

func assertNonMonotonicSumValue(t *testing.T, resourceMetrics metricdata.ResourceMetrics, name string, want float64) {
	t.Helper()
	metric := findMetric(resourceMetrics, name)
	if metric == nil {
		t.Fatalf("metric %q was not collected", name)
	}
	sum, ok := metric.Data.(metricdata.Sum[float64])
	if !ok {
		t.Fatalf("metric %q data type = %T, want float64 sum", name, metric.Data)
	}
	if sum.Temporality != metricdata.CumulativeTemporality || sum.IsMonotonic {
		t.Fatalf("metric %q sum = %#v, want cumulative non-monotonic", name, sum)
	}
	if len(sum.DataPoints) != 1 || sum.DataPoints[0].Value != want {
		t.Fatalf("metric %q points = %#v, want value %v", name, sum.DataPoints, want)
	}
}

func assertStringAttribute(
	t *testing.T,
	resourceMetrics metricdata.ResourceMetrics,
	metricName string,
	key string,
	want string,
) {
	t.Helper()
	metric := findMetric(resourceMetrics, metricName)
	sum := metric.Data.(metricdata.Sum[int64])
	value, found := sum.DataPoints[0].Attributes.Value(attribute.Key(key))
	if !found || value.AsString() != want {
		t.Fatalf("metric %q attribute %q = %v, %t; want %q", metricName, key, value, found, want)
	}
}

func assertNoDataPoints(t *testing.T, resourceMetrics metricdata.ResourceMetrics, name string) {
	t.Helper()
	metric := findMetric(resourceMetrics, name)
	if metric == nil {
		return
	}
	switch data := metric.Data.(type) {
	case metricdata.Gauge[float64]:
		if len(data.DataPoints) != 0 {
			t.Fatalf("metric %q retained points %#v", name, data.DataPoints)
		}
	case metricdata.Sum[float64]:
		if len(data.DataPoints) != 0 {
			t.Fatalf("metric %q retained points %#v", name, data.DataPoints)
		}
	default:
		t.Fatalf("metric %q data type = %T", name, metric.Data)
	}
}
