package telemetry_test

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/AaronWangTT/blobfuse-health-exporter/internal/metrics"
	"github.com/AaronWangTT/blobfuse-health-exporter/internal/source"
	"github.com/AaronWangTT/blobfuse-health-exporter/internal/telemetry"
	collectormetricspb "go.opentelemetry.io/proto/otlp/collector/metrics/v1"
	"google.golang.org/protobuf/proto"
)

func TestPipelineRetainsCumulativeAdditionsAfterExportFailure(t *testing.T) {
	var failExports atomic.Bool
	failExports.Store(true)
	successfulRequests := make(chan *collectormetricspb.ExportMetricsServiceRequest, 4)
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		body, err := io.ReadAll(request.Body)
		if err != nil {
			writer.WriteHeader(http.StatusBadRequest)
			return
		}
		if failExports.Load() {
			writer.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		payload := new(collectormetricspb.ExportMetricsServiceRequest)
		if err := proto.Unmarshal(body, payload); err != nil {
			writer.WriteHeader(http.StatusBadRequest)
			return
		}
		successfulRequests <- payload
		writer.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	state := metrics.NewState()
	processor, err := metrics.NewProcessor(state, nil)
	if err != nil {
		t.Fatalf("NewProcessor() error = %v", err)
	}
	processStorageRecord(t, processor, source.RecordModeBaseline, 100, 2)
	settings := pipelineConfig(server.URL + "/v1/metrics")
	settings.ExportTimeout = 40 * time.Millisecond
	pipeline, err := telemetry.NewPipeline(
		context.Background(),
		settings,
		telemetryIdentity(),
		processor,
		"test-version",
	)
	if err != nil {
		t.Fatalf("NewPipeline() error = %v", err)
	}
	t.Cleanup(func() { pipeline.Shutdown(context.Background()) })

	processStorageRecord(t, processor, source.RecordModeLive, 130, 2)
	firstContext, firstCancel := context.WithTimeout(context.Background(), time.Second)
	firstErr := pipeline.ForceFlush(firstContext)
	firstCancel()
	if firstErr == nil {
		t.Fatal("first ForceFlush() error = nil during receiver outage")
	}

	processStorageRecord(t, processor, source.RecordModeLive, 150, 2)
	failExports.Store(false)
	secondContext, secondCancel := context.WithTimeout(context.Background(), time.Second)
	secondErr := pipeline.ForceFlush(secondContext)
	secondCancel()
	if secondErr != nil {
		t.Fatalf("second ForceFlush() error = %v", secondErr)
	}

	select {
	case request := <-successfulRequests:
		assertOTLPStorageCounter(t, request, pipeline.Epoch(), 50)
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for recovered OTLP export")
	}
}
