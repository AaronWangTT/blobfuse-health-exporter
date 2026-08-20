package telemetry_test

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/AaronWangTT/blobfuse-health-exporter/internal/metrics"
	"github.com/AaronWangTT/blobfuse-health-exporter/internal/source"
	"github.com/AaronWangTT/blobfuse-health-exporter/internal/telemetry"
	collectormetricspb "go.opentelemetry.io/proto/otlp/collector/metrics/v1"
	"google.golang.org/protobuf/proto"
)

func TestPipelineExportsSelfMetricsWithSeparateResource(t *testing.T) {
	requests := make(chan *collectormetricspb.ExportMetricsServiceRequest, 4)
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		body, err := io.ReadAll(request.Body)
		if err != nil {
			writer.WriteHeader(http.StatusBadRequest)
			return
		}
		payload := new(collectormetricspb.ExportMetricsServiceRequest)
		if err := proto.Unmarshal(body, payload); err != nil {
			writer.WriteHeader(http.StatusBadRequest)
			return
		}
		requests <- payload
		writer.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	processor, err := metrics.NewProcessor(metrics.NewState(), nil)
	if err != nil {
		t.Fatalf("NewProcessor() error = %v", err)
	}
	processStorageRecord(t, processor, source.RecordModeBaseline, 100, 2)
	pipeline, err := telemetry.NewPipeline(
		context.Background(),
		pipelineConfig(server.URL+"/v1/metrics"),
		telemetryIdentity(),
		processor,
		"test-version",
	)
	if err != nil {
		t.Fatalf("NewPipeline() error = %v", err)
	}
	t.Cleanup(func() { pipeline.Shutdown(context.Background()) })

	processStorageRecord(t, processor, source.RecordModeLive, 130, 3)
	processStorageRecord(t, processor, source.RecordModeLive, 90, 4)
	pipeline.RecordSourceEvent(source.SourceEvent{Kind: source.SourceEventRotations, Count: 2})
	pipeline.RecordSourceEvent(source.SourceEvent{
		Kind:   source.SourceEventDiscontinuity,
		Count:  1,
		Reason: source.SourceDiscontinuityUncleanClose,
	})

	if err := pipeline.ForceFlush(context.Background()); err != nil {
		t.Fatalf("ForceFlush() error = %v", err)
	}
	target := receiveOTLPRequest(t, requests)
	self := receiveOTLPRequest(t, requests)
	assertOTLPResource(t, target)
	assertSelfResource(t, self)
	assertSelfCounter(t, self, "blobfuse_health_exporter.source.records", metrics.SelfAttributeOutcome, "accepted", 3)
	assertSelfCounter(t, self, "blobfuse_health_exporter.source.rotations", "", "", 2)
	assertSelfCounter(t, self, "blobfuse_health_exporter.source.discontinuities", metrics.SelfAttributeReason, "unclean_close", 1)
	assertSelfCounter(t, self, "blobfuse_health_exporter.source.counter_resets", metrics.SelfAttributeSourceMetric, "azure.blobfuse.storage.io", 1)
}

func receiveOTLPRequest(
	t *testing.T,
	requests <-chan *collectormetricspb.ExportMetricsServiceRequest,
) *collectormetricspb.ExportMetricsServiceRequest {
	t.Helper()
	select {
	case request := <-requests:
		return request
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for OTLP request")
		return nil
	}
}

func assertSelfResource(t *testing.T, request *collectormetricspb.ExportMetricsServiceRequest) {
	t.Helper()
	resources := request.GetResourceMetrics()
	if len(resources) != 1 {
		t.Fatalf("self resource metrics = %d, want 1", len(resources))
	}
	attributes := otlpAttributes(resources[0].GetResource().GetAttributes())
	if len(attributes) != 1 || attributes["service.name"].GetStringValue() != "blobfuse-health-exporter" {
		t.Fatalf("self resource attributes = %#v, want only adapter service.name", attributes)
	}
}

func assertSelfCounter(
	t *testing.T,
	request *collectormetricspb.ExportMetricsServiceRequest,
	name string,
	attributeName string,
	attributeValue string,
	want int64,
) {
	t.Helper()
	points := findOTLPMetric(t, request, name).GetSum().GetDataPoints()
	for _, point := range points {
		attributes := otlpAttributes(point.GetAttributes())
		if attributeName == "" {
			if len(attributes) == 0 && point.GetAsInt() == want {
				return
			}
			continue
		}
		if len(attributes) == 1 && attributes[attributeName].GetStringValue() == attributeValue && point.GetAsInt() == want {
			return
		}
	}
	t.Fatalf("self metric %q has no point %q=%q value %d", name, attributeName, attributeValue, want)
}
