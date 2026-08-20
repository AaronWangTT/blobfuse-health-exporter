package telemetry_test

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/AaronWangTT/blobfuse-health-exporter/internal/metrics"
	"github.com/AaronWangTT/blobfuse-health-exporter/internal/source"
	"github.com/AaronWangTT/blobfuse-health-exporter/internal/telemetry"
	collectormetricspb "go.opentelemetry.io/proto/otlp/collector/metrics/v1"
	commonpb "go.opentelemetry.io/proto/otlp/common/v1"
	metricspb "go.opentelemetry.io/proto/otlp/metrics/v1"
	"google.golang.org/protobuf/proto"
)

func TestPipelineExportsContractCompliantOTLPPayload(t *testing.T) {
	requests := make(chan *collectormetricspb.ExportMetricsServiceRequest, 4)
	serverErrors := make(chan error, 4)
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		body, err := io.ReadAll(request.Body)
		if err != nil {
			serverErrors <- err
			writer.WriteHeader(http.StatusBadRequest)
			return
		}
		payload := new(collectormetricspb.ExportMetricsServiceRequest)
		if err := proto.Unmarshal(body, payload); err != nil {
			serverErrors <- err
			writer.WriteHeader(http.StatusBadRequest)
			return
		}
		requests <- payload
		writer.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	state := metrics.NewState()
	processor, err := metrics.NewProcessor(state, nil)
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

	if err := pipeline.ForceFlush(context.Background()); err != nil {
		t.Fatalf("ForceFlush() error = %v", err)
	}
	select {
	case err := <-serverErrors:
		t.Fatalf("OTLP receiver error = %v", err)
	case request := <-requests:
		assertOTLPResource(t, request)
		assertOTLPScope(t, request)
		assertOTLPStorageCounter(t, request, pipeline.Epoch(), 30)
		assertOTLPOpenFilesGauge(t, request, 3)
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for OTLP request")
	}
}

func processStorageRecord(t *testing.T, processor *metrics.Processor, mode source.RecordMode, counter, openFiles int) {
	t.Helper()
	record := source.Record{Raw: []byte(`{
		"BlobfuseStats":[
			{"componentName":"azstorage","value":{"Bytes Downloaded":` + integerString(counter) + `}},
			{"componentName":"libfuse","value":{"OpenFileHandles":` + integerString(openFiles) + `}}
		]
	}`)}
	if err := processor.Process(mode, record); err != nil {
		t.Fatalf("Process() error = %v", err)
	}
}

func assertOTLPResource(t *testing.T, request *collectormetricspb.ExportMetricsServiceRequest) {
	t.Helper()
	resources := request.GetResourceMetrics()
	if len(resources) != 1 {
		t.Fatalf("resource metrics = %d, want 1", len(resources))
	}
	attributes := otlpAttributes(resources[0].GetResource().GetAttributes())
	if len(attributes) != 5 {
		t.Fatalf("resource attributes = %#v, want exactly 5", attributes)
	}
	wantStrings := map[string]string{
		"service.name":                   "blobfuse2",
		"service.instance.id":            telemetryIdentity().InstanceID(),
		"process.creation.time":          "2026-08-20T12:00:00Z",
		telemetry.MonitorSourceAttribute: "bfusemon_json",
	}
	for key, want := range wantStrings {
		if got := attributes[key].GetStringValue(); got != want {
			t.Fatalf("resource attribute %q = %q, want %q", key, got, want)
		}
	}
	if got := attributes["process.pid"].GetIntValue(); got != 1234 {
		t.Fatalf("process.pid = %d, want 1234", got)
	}
}

func assertOTLPScope(t *testing.T, request *collectormetricspb.ExportMetricsServiceRequest) {
	t.Helper()
	scopes := request.GetResourceMetrics()[0].GetScopeMetrics()
	if len(scopes) != 1 {
		t.Fatalf("scope metrics = %d, want 1", len(scopes))
	}
	if scope := scopes[0].GetScope(); scope.GetName() != telemetry.InstrumentationScopeName || scope.GetVersion() != "test-version" {
		t.Fatalf("instrumentation scope = %#v", scope)
	}
}

func assertOTLPStorageCounter(
	t *testing.T,
	request *collectormetricspb.ExportMetricsServiceRequest,
	epoch time.Time,
	want int64,
) {
	t.Helper()
	metric := findOTLPMetric(t, request, "azure.blobfuse.storage.io")
	sum := metric.GetSum()
	if sum == nil || !sum.GetIsMonotonic() || sum.GetAggregationTemporality() != metricspb.AggregationTemporality_AGGREGATION_TEMPORALITY_CUMULATIVE {
		t.Fatalf("storage sum = %#v, want cumulative monotonic", sum)
	}
	points := sum.GetDataPoints()
	if len(points) != 1 || points[0].GetAsInt() != want {
		t.Fatalf("storage points = %#v, want %d", points, want)
	}
	if start := time.Unix(0, int64(points[0].GetStartTimeUnixNano())).UTC(); start.Before(epoch) {
		t.Fatalf("counter start time %v is before pipeline epoch %v", start, epoch)
	}
	attributes := otlpAttributes(points[0].GetAttributes())
	if got := attributes[metrics.AttributeIODirection].GetStringValue(); got != "read" {
		t.Fatalf("I/O direction = %q, want read", got)
	}
}

func assertOTLPOpenFilesGauge(t *testing.T, request *collectormetricspb.ExportMetricsServiceRequest, want float64) {
	t.Helper()
	metric := findOTLPMetric(t, request, "azure.blobfuse.file.open")
	points := metric.GetGauge().GetDataPoints()
	if len(points) != 1 || points[0].GetAsDouble() != want {
		t.Fatalf("open-file points = %#v, want %v", points, want)
	}
	attributes := otlpAttributes(points[0].GetAttributes())
	if got := attributes[metrics.AttributeComponentName].GetStringValue(); got != "libfuse" {
		t.Fatalf("component = %q, want libfuse", got)
	}
}

func findOTLPMetric(
	t *testing.T,
	request *collectormetricspb.ExportMetricsServiceRequest,
	name string,
) *metricspb.Metric {
	t.Helper()
	for _, resourceMetrics := range request.GetResourceMetrics() {
		for _, scopeMetrics := range resourceMetrics.GetScopeMetrics() {
			for _, metric := range scopeMetrics.GetMetrics() {
				if metric.GetName() == name {
					return metric
				}
			}
		}
	}
	t.Fatalf("metric %q was not exported", name)
	return nil
}

func otlpAttributes(attributes []*commonpb.KeyValue) map[string]*commonpb.AnyValue {
	result := make(map[string]*commonpb.AnyValue, len(attributes))
	for _, attribute := range attributes {
		result[attribute.GetKey()] = attribute.GetValue()
	}
	return result
}

func integerString(value int) string {
	return fmt.Sprintf("%d", value)
}
