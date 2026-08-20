package telemetry

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"time"

	"github.com/AaronWangTT/blobfuse-health-exporter/internal/config"
	"go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetrichttp"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
)

const MaxOTLPRequestBytes = 1 * 1024 * 1024

func NewOTLPHTTPExporter(ctx context.Context, settings config.Config) (*otlpmetrichttp.Exporter, error) {
	if ctx == nil {
		return nil, fmt.Errorf("exporter context is required")
	}
	if err := settings.Validate(); err != nil {
		return nil, err
	}

	retryInitial := minDuration(time.Second, settings.ExportTimeout/4)
	if retryInitial <= 0 {
		retryInitial = time.Nanosecond
	}
	retryMaximum := minDuration(5*time.Second, settings.ExportTimeout/2)
	if retryMaximum < retryInitial {
		retryMaximum = retryInitial
	}

	exporter, err := otlpmetrichttp.New(
		ctx,
		otlpmetrichttp.WithEndpointURL(settings.OTLPEndpoint),
		otlpmetrichttp.WithInsecure(),
		otlpmetrichttp.WithHeaders(map[string]string{}),
		otlpmetrichttp.WithCompression(otlpmetrichttp.NoCompression),
		otlpmetrichttp.WithProxy(directProxy),
		otlpmetrichttp.WithTimeout(settings.ExportTimeout),
		otlpmetrichttp.WithRetry(otlpmetrichttp.RetryConfig{
			Enabled:         true,
			InitialInterval: retryInitial,
			MaxInterval:     retryMaximum,
			MaxElapsedTime:  settings.ExportTimeout,
		}),
		otlpmetrichttp.WithTemporalitySelector(sdkmetric.CumulativeTemporalitySelector),
		otlpmetrichttp.WithAggregationSelector(sdkmetric.DefaultAggregationSelector),
		otlpmetrichttp.WithMaxRequestSize(MaxOTLPRequestBytes),
	)
	if err != nil {
		return nil, fmt.Errorf("create OTLP HTTP exporter: %w", err)
	}
	return exporter, nil
}

func directProxy(*http.Request) (*url.URL, error) {
	return nil, nil
}

func minDuration(left, right time.Duration) time.Duration {
	if left < right {
		return left
	}
	return right
}
