package telemetry_test

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/AaronWangTT/blobfuse-health-exporter/internal/config"
	"github.com/AaronWangTT/blobfuse-health-exporter/internal/telemetry"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
)

func TestNewOTLPHTTPExporterOverridesEnvironmentConfiguration(t *testing.T) {
	var configuredRequests atomic.Int32
	requestDetails := make(chan *http.Request, 1)
	configuredServer := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		configuredRequests.Add(1)
		_, _ = io.Copy(io.Discard, request.Body)
		requestDetails <- request.Clone(context.Background())
		writer.WriteHeader(http.StatusOK)
	}))
	defer configuredServer.Close()

	var environmentRequests atomic.Int32
	environmentServer := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		environmentRequests.Add(1)
		writer.WriteHeader(http.StatusOK)
	}))
	defer environmentServer.Close()

	t.Setenv("OTEL_EXPORTER_OTLP_METRICS_ENDPOINT", environmentServer.URL+"/v1/metrics")
	t.Setenv("OTEL_EXPORTER_OTLP_METRICS_HEADERS", "x-secret=environment-secret")
	t.Setenv("OTEL_EXPORTER_OTLP_METRICS_COMPRESSION", "gzip")
	t.Setenv("OTEL_EXPORTER_OTLP_METRICS_TEMPORALITY_PREFERENCE", "delta")
	t.Setenv("HTTP_PROXY", "http://127.0.0.1:1")

	settings := validTelemetryConfig(configuredServer.URL + "/v1/metrics")
	exporter, err := telemetry.NewOTLPHTTPExporter(context.Background(), settings)
	if err != nil {
		t.Fatalf("NewOTLPHTTPExporter() error = %v", err)
	}
	t.Cleanup(func() { exporter.Shutdown(context.Background()) })

	if err := exporter.Export(context.Background(), &metricdata.ResourceMetrics{}); err != nil {
		t.Fatalf("Export() error = %v", err)
	}
	if configuredRequests.Load() != 1 || environmentRequests.Load() != 0 {
		t.Fatalf("configured requests = %d, environment requests = %d", configuredRequests.Load(), environmentRequests.Load())
	}
	request := <-requestDetails
	if request.URL.Path != "/v1/metrics" {
		t.Fatalf("request path = %q, want /v1/metrics", request.URL.Path)
	}
	if request.Header.Get("x-secret") != "" {
		t.Fatalf("environment header was sent: %q", request.Header.Get("x-secret"))
	}
	if request.Header.Get("Content-Encoding") != "" {
		t.Fatalf("environment compression was used: %q", request.Header.Get("Content-Encoding"))
	}
	if got := exporter.Temporality(sdkmetric.InstrumentKindCounter); got != metricdata.CumulativeTemporality {
		t.Fatalf("counter temporality = %v, want cumulative", got)
	}
}

func TestNewOTLPHTTPExporterRejectsInvalidConfigurationBeforeNetwork(t *testing.T) {
	settings := validTelemetryConfig("http://example.com/v1/metrics")
	if _, err := telemetry.NewOTLPHTTPExporter(context.Background(), settings); err == nil {
		t.Fatal("NewOTLPHTTPExporter() error = nil")
	}
}

func validTelemetryConfig(endpoint string) config.Config {
	settings := config.Default()
	settings.ReportDirectory = "/reports"
	settings.PID = 1234
	settings.OTLPEndpoint = endpoint
	settings.ExportInterval = time.Second
	settings.ExportTimeout = 100 * time.Millisecond
	return settings
}
