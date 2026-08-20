package telemetry

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/AaronWangTT/blobfuse-health-exporter/internal/metrics"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
)

type resourceExporter struct {
	exporter   sdkmetric.Exporter
	selfReader *sdkmetric.ManualReader
	onError    func(metrics.ExportErrorType)
	mutex      sync.Mutex
}

func newResourceExporter(
	exporter sdkmetric.Exporter,
	selfReader *sdkmetric.ManualReader,
	onError func(metrics.ExportErrorType),
) (*resourceExporter, error) {
	if exporter == nil {
		return nil, fmt.Errorf("metric exporter is required")
	}
	if selfReader == nil {
		return nil, fmt.Errorf("self-metric reader is required")
	}
	return &resourceExporter{exporter: exporter, selfReader: selfReader, onError: onError}, nil
}

func (exporter *resourceExporter) Temporality(kind sdkmetric.InstrumentKind) metricdata.Temporality {
	return exporter.exporter.Temporality(kind)
}

func (exporter *resourceExporter) Aggregation(kind sdkmetric.InstrumentKind) sdkmetric.Aggregation {
	return exporter.exporter.Aggregation(kind)
}

func (exporter *resourceExporter) Export(ctx context.Context, target *metricdata.ResourceMetrics) error {
	if target == nil {
		return fmt.Errorf("target resource metrics are required")
	}

	exporter.mutex.Lock()
	defer exporter.mutex.Unlock()

	targetErr := exporter.export(ctx, target)
	var self metricdata.ResourceMetrics
	collectErr := exporter.selfReader.Collect(ctx, &self)
	if collectErr != nil || !hasMetricData(self) {
		return errors.Join(targetErr, collectErr)
	}
	return errors.Join(targetErr, exporter.export(ctx, &self))
}

func (exporter *resourceExporter) ForceFlush(ctx context.Context) error {
	exporter.mutex.Lock()
	defer exporter.mutex.Unlock()
	err := exporter.exporter.ForceFlush(ctx)
	exporter.recordError(classifyExportError(ctx, err), err)
	return err
}

func (exporter *resourceExporter) Shutdown(ctx context.Context) error {
	exporter.mutex.Lock()
	defer exporter.mutex.Unlock()
	err := exporter.exporter.Shutdown(ctx)
	exporter.recordError(metrics.ExportErrorShutdown, err)
	return err
}

func (exporter *resourceExporter) export(ctx context.Context, data *metricdata.ResourceMetrics) error {
	err := exporter.exporter.Export(ctx, data)
	if err == nil {
		return nil
	}
	exporter.recordError(classifyExportError(ctx, err), err)
	return err
}

func (exporter *resourceExporter) recordError(errorType metrics.ExportErrorType, err error) {
	if err != nil && exporter.onError != nil {
		exporter.onError(errorType)
	}
}

func classifyExportError(ctx context.Context, err error) metrics.ExportErrorType {
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return metrics.ExportErrorTimeout
	}
	return metrics.ExportErrorTransport
}

func hasMetricData(resourceMetrics metricdata.ResourceMetrics) bool {
	for _, scopeMetrics := range resourceMetrics.ScopeMetrics {
		if len(scopeMetrics.Metrics) > 0 {
			return true
		}
	}
	return false
}

var _ sdkmetric.Exporter = (*resourceExporter)(nil)
