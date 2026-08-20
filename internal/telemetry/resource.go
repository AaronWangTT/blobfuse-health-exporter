package telemetry

import (
	"fmt"
	"time"

	"github.com/AaronWangTT/blobfuse-health-exporter/internal/source"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/sdk/resource"
	semconv "go.opentelemetry.io/otel/semconv/v1.37.0"
)

const MonitorSourceAttribute = "azure.blobfuse.monitor.source"

func NewAdapterResource() *resource.Resource {
	return resource.NewWithAttributes(
		semconv.SchemaURL,
		semconv.ServiceName("blobfuse-health-exporter"),
	)
}

// NewBlobFuseResource creates only the resource attributes allowed by the v0
// contract. It does not merge the SDK default or environment resource.
func NewBlobFuseResource(identity source.ProcessIdentity) (*resource.Resource, error) {
	if identity.BootID == "" || identity.PID <= 0 || identity.StartTicks == 0 || identity.CreationTime.IsZero() {
		return nil, fmt.Errorf("process identity is incomplete")
	}

	return resource.NewWithAttributes(
		semconv.SchemaURL,
		semconv.ServiceName("blobfuse2"),
		semconv.ServiceInstanceID(identity.InstanceID()),
		semconv.ProcessPID(identity.PID),
		semconv.ProcessCreationTime(identity.CreationTime.UTC().Format(time.RFC3339Nano)),
		attribute.String(MonitorSourceAttribute, "bfusemon_json"),
	), nil
}
