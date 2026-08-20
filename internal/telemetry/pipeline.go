package telemetry

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/AaronWangTT/blobfuse-health-exporter/internal/config"
	"github.com/AaronWangTT/blobfuse-health-exporter/internal/metrics"
	"github.com/AaronWangTT/blobfuse-health-exporter/internal/source"
	otelmetric "go.opentelemetry.io/otel/metric"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
)

const (
	InstrumentationScopeName = "blobfuse-health-exporter"
	MetricCardinalityLimit   = 32
)

type Pipeline struct {
	mutex        sync.Mutex
	provider     *sdkmetric.MeterProvider
	selfProvider *sdkmetric.MeterProvider
	recorder     *metrics.OTelRecorder
	selfRecorder *metrics.SelfRecorder
	epoch        time.Time
	closed       bool
}

// NewPipeline creates the MeterProvider after source baseline cutover. Its
// provider lifetime defines the cumulative metric epoch for this adapter run.
func NewPipeline(
	ctx context.Context,
	settings config.Config,
	identity source.ProcessIdentity,
	processor *metrics.Processor,
	version string,
) (*Pipeline, error) {
	if ctx == nil {
		return nil, fmt.Errorf("pipeline context is required")
	}
	if processor == nil || processor.State() == nil {
		return nil, fmt.Errorf("metric processor is required")
	}
	if version == "" {
		return nil, fmt.Errorf("instrumentation version is required")
	}

	targetResource, err := NewBlobFuseResource(identity)
	if err != nil {
		return nil, err
	}
	transport, err := NewOTLPHTTPExporter(ctx, settings)
	if err != nil {
		return nil, err
	}
	selfReader := sdkmetric.NewManualReader(
		sdkmetric.WithTemporalitySelector(sdkmetric.CumulativeTemporalitySelector),
		sdkmetric.WithAggregationSelector(sdkmetric.DefaultAggregationSelector),
	)
	selfProvider := sdkmetric.NewMeterProvider(
		sdkmetric.WithReader(selfReader),
		sdkmetric.WithResource(NewAdapterResource()),
		sdkmetric.WithCardinalityLimit(MetricCardinalityLimit),
	)
	selfMeter := selfProvider.Meter(
		InstrumentationScopeName,
		otelmetric.WithInstrumentationVersion(version),
	)
	selfRecorder, err := metrics.NewSelfRecorder(selfMeter)
	if err != nil {
		selfProvider.Shutdown(context.Background())
		transport.Shutdown(context.Background())
		return nil, err
	}
	selfRecorder.RecordProcessingStats(processor.Stats())
	exporter, err := newResourceExporter(transport, selfReader, selfRecorder.RecordExportError)
	if err != nil {
		selfProvider.Shutdown(context.Background())
		transport.Shutdown(context.Background())
		return nil, err
	}
	reader := sdkmetric.NewPeriodicReader(
		exporter,
		sdkmetric.WithInterval(settings.ExportInterval),
		sdkmetric.WithTimeout(settings.ExportTimeout),
	)

	epoch := time.Now().UTC()
	provider := sdkmetric.NewMeterProvider(
		sdkmetric.WithReader(reader),
		sdkmetric.WithResource(targetResource),
		sdkmetric.WithCardinalityLimit(MetricCardinalityLimit),
	)
	meter := provider.Meter(
		InstrumentationScopeName,
		otelmetric.WithInstrumentationVersion(version),
	)
	recorder, err := metrics.NewOTelRecorder(meter, processor.State())
	if err != nil {
		provider.Shutdown(context.Background())
		selfProvider.Shutdown(context.Background())
		return nil, err
	}
	if err := processor.AttachObserver(func(processed metrics.ProcessedRecord) {
		recorder.Record(processed)
		selfRecorder.Record(processed)
	}); err != nil {
		recorder.Close()
		provider.Shutdown(context.Background())
		selfProvider.Shutdown(context.Background())
		return nil, err
	}

	return &Pipeline{
		provider:     provider,
		selfProvider: selfProvider,
		recorder:     recorder,
		selfRecorder: selfRecorder,
		epoch:        epoch,
	}, nil
}

func (pipeline *Pipeline) Epoch() time.Time {
	if pipeline == nil {
		return time.Time{}
	}
	return pipeline.epoch
}

func (pipeline *Pipeline) ForceFlush(ctx context.Context) error {
	if pipeline == nil || pipeline.provider == nil {
		return fmt.Errorf("telemetry pipeline is not initialized")
	}
	return pipeline.provider.ForceFlush(ctx)
}

func (pipeline *Pipeline) Shutdown(ctx context.Context) error {
	if pipeline == nil {
		return nil
	}
	pipeline.mutex.Lock()
	defer pipeline.mutex.Unlock()
	if pipeline.closed {
		return nil
	}
	pipeline.closed = true

	var shutdownErrors []error
	if err := pipeline.provider.ForceFlush(ctx); err != nil {
		shutdownErrors = append(shutdownErrors, err)
	}
	if err := pipeline.recorder.Close(); err != nil {
		shutdownErrors = append(shutdownErrors, err)
	}
	if err := pipeline.provider.Shutdown(ctx); err != nil {
		shutdownErrors = append(shutdownErrors, err)
	}
	if err := pipeline.selfProvider.Shutdown(ctx); err != nil {
		shutdownErrors = append(shutdownErrors, err)
	}
	return errors.Join(shutdownErrors...)
}
