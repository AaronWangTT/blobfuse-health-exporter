package metrics_test

import (
	"context"
	"testing"

	"github.com/AaronWangTT/blobfuse-health-exporter/internal/metrics"
	"github.com/AaronWangTT/blobfuse-health-exporter/internal/source"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
)

func TestSelfRecorderExportsOnlyContractMetricsAndAttributes(t *testing.T) {
	reader := sdkmetric.NewManualReader()
	provider := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	t.Cleanup(func() { provider.Shutdown(context.Background()) })
	recorder, err := metrics.NewSelfRecorder(provider.Meter("test"))
	if err != nil {
		t.Fatalf("NewSelfRecorder() error = %v", err)
	}

	recorder.RecordProcessingStats(metrics.ProcessingStats{
		AcceptedRecords: 2,
		IgnoredRecords:  3,
		InvalidRecords:  4,
	})
	recorder.Record(metrics.ProcessedRecord{
		Outcome: metrics.RecordAccepted,
		Metrics: metrics.ApplyResult{CounterResets: []metrics.CounterReset{
			{Series: source.Series{Metric: source.MetricStorageIO, Direction: source.DirectionRead}},
			{Series: source.Series{Metric: source.MetricCacheHits}},
		}},
	})
	recorder.RecordRotations(2)
	for _, reason := range []metrics.DiscontinuityReason{
		metrics.DiscontinuityGenerationMissing,
		metrics.DiscontinuityGenerationTruncated,
		metrics.DiscontinuityOversizeRecord,
		metrics.DiscontinuityStaleGeneration,
		metrics.DiscontinuityUncleanClose,
	} {
		recorder.RecordDiscontinuity(reason)
	}
	for _, errorType := range []metrics.ExportErrorType{
		metrics.ExportErrorTimeout,
		metrics.ExportErrorTransport,
		metrics.ExportErrorShutdown,
	} {
		recorder.RecordExportError(errorType)
	}
	recorder.RecordDiscontinuity(metrics.DiscontinuityReason("private-path"))
	recorder.RecordExportError(metrics.ExportErrorType("raw-error"))

	var data metricdata.ResourceMetrics
	if err := reader.Collect(context.Background(), &data); err != nil {
		t.Fatalf("Collect() error = %v", err)
	}

	assertSelfPoints(t, findMetricData(t, data, "blobfuse_health_exporter.source.records"), metrics.SelfAttributeOutcome, map[string]int64{
		"accepted": 3,
		"ignored":  3,
		"invalid":  4,
	})
	assertSelfPoints(t, findMetricData(t, data, "blobfuse_health_exporter.source.rotations"), "", map[string]int64{"": 2})
	assertSelfPoints(t, findMetricData(t, data, "blobfuse_health_exporter.source.discontinuities"), metrics.SelfAttributeReason, map[string]int64{
		"generation_missing":   1,
		"generation_truncated": 1,
		"oversize_record":      1,
		"stale_generation":     1,
		"unclean_close":        1,
	})
	assertSelfPoints(t, findMetricData(t, data, "blobfuse_health_exporter.source.counter_resets"), metrics.SelfAttributeSourceMetric, map[string]int64{
		"azure.blobfuse.storage.io": 1,
		"azure.blobfuse.cache.hits": 1,
	})
	assertSelfPoints(t, findMetricData(t, data, "blobfuse_health_exporter.export.errors"), metrics.SelfAttributeErrorType, map[string]int64{
		"timeout":   1,
		"transport": 1,
		"shutdown":  1,
	})
}

func findMetricData(t *testing.T, data metricdata.ResourceMetrics, name string) metricdata.Metrics {
	t.Helper()
	for _, scope := range data.ScopeMetrics {
		for _, metric := range scope.Metrics {
			if metric.Name == name {
				return metric
			}
		}
	}
	t.Fatalf("metric %q was not collected", name)
	return metricdata.Metrics{}
}

func assertSelfPoints(t *testing.T, metric metricdata.Metrics, attributeName string, want map[string]int64) {
	t.Helper()
	sum, ok := metric.Data.(metricdata.Sum[int64])
	if !ok || !sum.IsMonotonic || sum.Temporality != metricdata.CumulativeTemporality {
		t.Fatalf("metric %q data = %#v, want cumulative monotonic sum", metric.Name, metric.Data)
	}
	got := make(map[string]int64, len(sum.DataPoints))
	for _, point := range sum.DataPoints {
		attributeValue := ""
		attributes := point.Attributes.ToSlice()
		if attributeName == "" {
			if len(attributes) != 0 {
				t.Fatalf("metric %q attributes = %#v, want none", metric.Name, attributes)
			}
		} else {
			if len(attributes) != 1 || string(attributes[0].Key) != attributeName {
				t.Fatalf("metric %q attributes = %#v, want only %q", metric.Name, attributes, attributeName)
			}
			attributeValue = attributes[0].Value.AsString()
		}
		got[attributeValue] = point.Value
	}
	if len(got) != len(want) {
		t.Fatalf("metric %q points = %#v, want %#v", metric.Name, got, want)
	}
	for key, value := range want {
		if got[key] != value {
			t.Fatalf("metric %q point %q = %d, want %d", metric.Name, key, got[key], value)
		}
	}
}
