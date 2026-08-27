//go:build linux

package metrics

import (
	"context"
	"errors"
	"testing"

	"github.com/AaronWangTT/blobfuse-health-exporter/internal/source"
	otelmetric "go.opentelemetry.io/otel/metric"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
)

func TestProcessRecorderExportsCPUTimeAndResidentMemory(t *testing.T) {
	reader := sdkmetric.NewManualReader(
		sdkmetric.WithTemporalitySelector(sdkmetric.CumulativeTemporalitySelector),
	)
	provider := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	t.Cleanup(func() { provider.Shutdown(context.Background()) })
	identity := source.ProcessIdentity{PID: 1234, StartTicks: 250}
	readCalls := 0
	recorder, err := newProcessRecorder(
		provider.Meter("blobfuse-health-exporter", otelmetric.WithInstrumentationVersion("test")),
		identity,
		func(got source.ProcessIdentity) (source.ProcessMetrics, error) {
			readCalls++
			if !got.SameSession(identity) {
				t.Fatalf("identity = %#v, want %#v", got, identity)
			}
			return source.ProcessMetrics{
				CPUUserSeconds:   1.25,
				CPUSystemSeconds: 0.75,
				MemoryUsageBytes: 42 * 4096,
			}, nil
		},
	)
	if err != nil {
		t.Fatalf("newProcessRecorder() error = %v", err)
	}
	t.Cleanup(func() { recorder.Close() })

	collected := collectProcessMetrics(t, reader)
	if readCalls != 1 {
		t.Fatalf("read calls = %d, want 1", readCalls)
	}
	assertProcessCPUTime(t, collected, map[string]float64{
		ProcessCPUStateUser:   1.25,
		ProcessCPUStateSystem: 0.75,
	})
	assertProcessMemoryUsage(t, collected, 42*4096)
	if recorder.ReadErrors() != 0 {
		t.Fatalf("ReadErrors() = %d, want 0", recorder.ReadErrors())
	}
}

func TestProcessRecorderReadFailureOmitsOnlyProcessMetrics(t *testing.T) {
	reader := sdkmetric.NewManualReader(
		sdkmetric.WithTemporalitySelector(sdkmetric.CumulativeTemporalitySelector),
	)
	provider := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	t.Cleanup(func() { provider.Shutdown(context.Background()) })
	recorder, err := newProcessRecorder(
		provider.Meter("blobfuse-health-exporter"),
		source.ProcessIdentity{PID: 1234, StartTicks: 250},
		func(source.ProcessIdentity) (source.ProcessMetrics, error) {
			return source.ProcessMetrics{}, errors.New("procfs unavailable")
		},
	)
	if err != nil {
		t.Fatalf("newProcessRecorder() error = %v", err)
	}
	t.Cleanup(func() { recorder.Close() })

	collected := collectProcessMetrics(t, reader)
	assertProcessMetricHasNoPoints(t, collected, ProcessCPUTimeName)
	assertProcessMetricHasNoPoints(t, collected, ProcessMemoryUsageName)
	if recorder.ReadErrors() != 1 {
		t.Fatalf("ReadErrors() = %d, want 1", recorder.ReadErrors())
	}
}

func collectProcessMetrics(t *testing.T, reader *sdkmetric.ManualReader) metricdata.ResourceMetrics {
	t.Helper()
	var collected metricdata.ResourceMetrics
	if err := reader.Collect(context.Background(), &collected); err != nil {
		t.Fatalf("Collect() error = %v", err)
	}
	return collected
}

func findProcessMetric(collected metricdata.ResourceMetrics, name string) *metricdata.Metrics {
	for _, scopeMetrics := range collected.ScopeMetrics {
		for index := range scopeMetrics.Metrics {
			if scopeMetrics.Metrics[index].Name == name {
				return &scopeMetrics.Metrics[index]
			}
		}
	}
	return nil
}

func assertProcessCPUTime(
	t *testing.T,
	collected metricdata.ResourceMetrics,
	want map[string]float64,
) {
	t.Helper()
	metric := findProcessMetric(collected, ProcessCPUTimeName)
	if metric == nil || metric.Unit != ProcessCPUTimeUnit || metric.Description != ProcessCPUTimeDescription {
		t.Fatalf("CPU metric = %#v", metric)
	}
	sum, ok := metric.Data.(metricdata.Sum[float64])
	if !ok || !sum.IsMonotonic || sum.Temporality != metricdata.CumulativeTemporality {
		t.Fatalf("CPU data = %#v", metric.Data)
	}
	if len(sum.DataPoints) != len(want) {
		t.Fatalf("CPU points = %#v", sum.DataPoints)
	}
	for _, point := range sum.DataPoints {
		state, found := point.Attributes.Value(AttributeProcessCPUState)
		if !found || point.Value != want[state.AsString()] {
			t.Fatalf("CPU point = %#v", point)
		}
	}
}

func assertProcessMemoryUsage(t *testing.T, collected metricdata.ResourceMetrics, want int64) {
	t.Helper()
	metric := findProcessMetric(collected, ProcessMemoryUsageName)
	if metric == nil || metric.Unit != ProcessMemoryUsageUnit || metric.Description != ProcessMemoryDescription {
		t.Fatalf("memory metric = %#v", metric)
	}
	sum, ok := metric.Data.(metricdata.Sum[int64])
	if !ok || sum.IsMonotonic || sum.Temporality != metricdata.CumulativeTemporality {
		t.Fatalf("memory data = %#v", metric.Data)
	}
	if len(sum.DataPoints) != 1 || sum.DataPoints[0].Value != want {
		t.Fatalf("memory points = %#v, want %d", sum.DataPoints, want)
	}
}

func assertProcessMetricHasNoPoints(t *testing.T, collected metricdata.ResourceMetrics, name string) {
	t.Helper()
	metric := findProcessMetric(collected, name)
	if metric == nil {
		return
	}
	switch data := metric.Data.(type) {
	case metricdata.Sum[float64]:
		if len(data.DataPoints) != 0 {
			t.Fatalf("metric %q retained points %#v", name, data.DataPoints)
		}
	case metricdata.Sum[int64]:
		if len(data.DataPoints) != 0 {
			t.Fatalf("metric %q retained points %#v", name, data.DataPoints)
		}
	default:
		t.Fatalf("metric %q data type = %T", name, metric.Data)
	}
}
