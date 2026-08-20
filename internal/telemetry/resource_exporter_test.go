package telemetry

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/AaronWangTT/blobfuse-health-exporter/internal/metrics"
	"go.opentelemetry.io/otel/attribute"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
	"go.opentelemetry.io/otel/sdk/resource"
)

func TestResourceExporterExportsTargetAndSelfResourcesSerially(t *testing.T) {
	selfReader := sdkmetric.NewManualReader()
	selfProvider := sdkmetric.NewMeterProvider(
		sdkmetric.WithReader(selfReader),
		sdkmetric.WithResource(serviceResource("blobfuse-health-exporter")),
	)
	t.Cleanup(func() { selfProvider.Shutdown(context.Background()) })
	counter, err := selfProvider.Meter("test").Int64Counter("test.self.counter")
	if err != nil {
		t.Fatalf("Int64Counter() error = %v", err)
	}
	counter.Add(context.Background(), 1)

	transport := &recordingMetricExporter{}
	exporter, err := newResourceExporter(transport, selfReader, nil)
	if err != nil {
		t.Fatalf("newResourceExporter() error = %v", err)
	}
	target := metricdata.ResourceMetrics{Resource: serviceResource("blobfuse2")}

	var wait sync.WaitGroup
	wait.Add(2)
	for range 2 {
		go func() {
			defer wait.Done()
			if err := exporter.Export(context.Background(), &target); err != nil {
				t.Errorf("Export() error = %v", err)
			}
		}()
	}
	wait.Wait()

	if got := transport.maxActive.Load(); got != 1 {
		t.Fatalf("maximum concurrent exports = %d, want 1", got)
	}
	if got, want := transport.resources, []string{
		"blobfuse2",
		"blobfuse-health-exporter",
		"blobfuse2",
		"blobfuse-health-exporter",
	}; !equalStrings(got, want) {
		t.Fatalf("exported resources = %v, want %v", got, want)
	}
}

func TestResourceExporterSkipsEmptySelfResource(t *testing.T) {
	selfReader := sdkmetric.NewManualReader()
	selfProvider := sdkmetric.NewMeterProvider(
		sdkmetric.WithReader(selfReader),
		sdkmetric.WithResource(serviceResource("blobfuse-health-exporter")),
	)
	t.Cleanup(func() { selfProvider.Shutdown(context.Background()) })

	transport := &recordingMetricExporter{}
	exporter, err := newResourceExporter(transport, selfReader, nil)
	if err != nil {
		t.Fatalf("newResourceExporter() error = %v", err)
	}
	target := metricdata.ResourceMetrics{Resource: serviceResource("blobfuse2")}
	if err := exporter.Export(context.Background(), &target); err != nil {
		t.Fatalf("Export() error = %v", err)
	}
	if got, want := transport.resources, []string{"blobfuse2"}; !equalStrings(got, want) {
		t.Fatalf("exported resources = %v, want %v", got, want)
	}
}

func TestResourceExporterClassifiesBoundedErrors(t *testing.T) {
	selfReader := sdkmetric.NewManualReader()
	selfProvider := sdkmetric.NewMeterProvider(
		sdkmetric.WithReader(selfReader),
		sdkmetric.WithResource(serviceResource("blobfuse-health-exporter")),
	)
	t.Cleanup(func() { selfProvider.Shutdown(context.Background()) })
	counter, err := selfProvider.Meter("test").Int64Counter("test.self.counter")
	if err != nil {
		t.Fatalf("Int64Counter() error = %v", err)
	}
	counter.Add(context.Background(), 1)

	transport := &recordingMetricExporter{
		exportErrors:  []error{context.DeadlineExceeded, errors.New("private transport error")},
		forceFlushErr: context.DeadlineExceeded,
		shutdownErr:   errors.New("private shutdown error"),
	}
	var errorTypes []metrics.ExportErrorType
	exporter, err := newResourceExporter(transport, selfReader, func(errorType metrics.ExportErrorType) {
		errorTypes = append(errorTypes, errorType)
	})
	if err != nil {
		t.Fatalf("newResourceExporter() error = %v", err)
	}
	target := metricdata.ResourceMetrics{Resource: serviceResource("blobfuse2")}
	if err := exporter.Export(context.Background(), &target); err == nil {
		t.Fatal("Export() error = nil")
	}
	if err := exporter.ForceFlush(context.Background()); err == nil {
		t.Fatal("ForceFlush() error = nil")
	}
	if err := exporter.Shutdown(context.Background()); err == nil {
		t.Fatal("Shutdown() error = nil")
	}

	want := []metrics.ExportErrorType{
		metrics.ExportErrorTimeout,
		metrics.ExportErrorTransport,
		metrics.ExportErrorTimeout,
		metrics.ExportErrorShutdown,
	}
	if len(errorTypes) != len(want) {
		t.Fatalf("error types = %v, want %v", errorTypes, want)
	}
	for index := range want {
		if errorTypes[index] != want[index] {
			t.Fatalf("error types = %v, want %v", errorTypes, want)
		}
	}
}

type recordingMetricExporter struct {
	mutex         sync.Mutex
	resources     []string
	active        atomic.Int32
	maxActive     atomic.Int32
	exportErrors  []error
	forceFlushErr error
	shutdownErr   error
}

func (exporter *recordingMetricExporter) Temporality(sdkmetric.InstrumentKind) metricdata.Temporality {
	return metricdata.CumulativeTemporality
}

func (exporter *recordingMetricExporter) Aggregation(sdkmetric.InstrumentKind) sdkmetric.Aggregation {
	return sdkmetric.AggregationDefault{}
}

func (exporter *recordingMetricExporter) Export(_ context.Context, data *metricdata.ResourceMetrics) error {
	active := exporter.active.Add(1)
	for active > exporter.maxActive.Load() && !exporter.maxActive.CompareAndSwap(exporter.maxActive.Load(), active) {
	}
	time.Sleep(time.Millisecond)

	name, _ := data.Resource.Set().Value(attribute.Key("service.name"))
	exporter.mutex.Lock()
	exporter.resources = append(exporter.resources, name.AsString())
	var err error
	if len(exporter.exportErrors) > 0 {
		err = exporter.exportErrors[0]
		exporter.exportErrors = exporter.exportErrors[1:]
	}
	exporter.mutex.Unlock()
	exporter.active.Add(-1)
	return err
}

func (exporter *recordingMetricExporter) ForceFlush(context.Context) error {
	return exporter.forceFlushErr
}

func (exporter *recordingMetricExporter) Shutdown(context.Context) error {
	return exporter.shutdownErr
}

func serviceResource(name string) *resource.Resource {
	return resource.NewSchemaless(attribute.String("service.name", name))
}

func equalStrings(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

var _ sdkmetric.Exporter = (*recordingMetricExporter)(nil)
