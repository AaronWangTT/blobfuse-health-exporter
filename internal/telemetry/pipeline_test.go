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
	"github.com/AaronWangTT/blobfuse-health-exporter/internal/metrics"
	"github.com/AaronWangTT/blobfuse-health-exporter/internal/source"
	"github.com/AaronWangTT/blobfuse-health-exporter/internal/telemetry"
)

func TestNewPipelineAttachesAfterBaselineAndExportsLiveMetrics(t *testing.T) {
	var requests atomic.Int32
	var activeRequests atomic.Int32
	var maxActiveRequests atomic.Int32
	contentTypes := make(chan string, 4)
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		active := activeRequests.Add(1)
		for active > maxActiveRequests.Load() && !maxActiveRequests.CompareAndSwap(maxActiveRequests.Load(), active) {
		}
		defer activeRequests.Add(-1)
		requests.Add(1)
		contentTypes <- request.Header.Get("Content-Type")
		_, _ = io.Copy(io.Discard, request.Body)
		writer.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	state := metrics.NewState()
	processor, err := metrics.NewProcessor(state, nil)
	if err != nil {
		t.Fatalf("NewProcessor() error = %v", err)
	}
	if err := processor.Process(source.RecordModeBaseline, source.Record{Raw: []byte(`{
		"BlobfuseStats":[
			{"componentName":"azstorage","value":{"Bytes Downloaded":100}},
			{"componentName":"libfuse","value":{"OpenFileHandles":2}}
		]
	}`)}); err != nil {
		t.Fatalf("baseline Process() error = %v", err)
	}

	settings := pipelineConfig(server.URL + "/v1/metrics")
	identity := telemetryIdentity()
	before := time.Now().UTC()
	pipeline, err := telemetry.NewPipeline(context.Background(), settings, identity, processor, "test-version")
	if err != nil {
		t.Fatalf("NewPipeline() error = %v", err)
	}
	after := time.Now().UTC()
	if pipeline.Epoch().Before(before) || pipeline.Epoch().After(after) {
		t.Fatalf("Epoch() = %v, want between %v and %v", pipeline.Epoch(), before, after)
	}

	if err := processor.Process(source.RecordModeLive, source.Record{Raw: []byte(`{
		"BlobfuseStats":[{"componentName":"azstorage","value":{"Bytes Downloaded":130}}]
	}`)}); err != nil {
		t.Fatalf("live Process() error = %v", err)
	}
	flushContext, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := pipeline.ForceFlush(flushContext); err != nil {
		t.Fatalf("ForceFlush() error = %v", err)
	}
	if requests.Load() != 2 {
		t.Fatalf("requests = %d, want target and self-resource exports", requests.Load())
	}
	if maxActiveRequests.Load() != 1 {
		t.Fatalf("maximum concurrent requests = %d, want 1", maxActiveRequests.Load())
	}
	for range 2 {
		if got := <-contentTypes; got != "application/x-protobuf" {
			t.Fatalf("Content-Type = %q, want application/x-protobuf", got)
		}
	}
	if err := pipeline.Shutdown(flushContext); err != nil {
		t.Fatalf("Shutdown() error = %v", err)
	}
	if err := pipeline.Shutdown(flushContext); err != nil {
		t.Fatalf("second Shutdown() error = %v", err)
	}
}

func pipelineConfig(endpoint string) config.Config {
	settings := config.Default()
	settings.ReportDirectory = "/reports"
	settings.PID = 1234
	settings.OTLPEndpoint = endpoint
	settings.ExportInterval = time.Hour
	settings.ExportTimeout = time.Second
	return settings
}

func telemetryIdentity() source.ProcessIdentity {
	return source.ProcessIdentity{
		BootID:       "4c1f5f44-7e22-4b5a-91d9-16d2d55f5c81",
		PID:          1234,
		StartTicks:   250,
		CreationTime: time.Date(2026, time.August, 20, 12, 0, 0, 0, time.UTC),
	}
}
