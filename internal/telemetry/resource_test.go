package telemetry_test

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/AaronWangTT/blobfuse-health-exporter/internal/source"
	"github.com/AaronWangTT/blobfuse-health-exporter/internal/telemetry"
	"go.opentelemetry.io/otel/attribute"
	semconv "go.opentelemetry.io/otel/semconv/v1.37.0"
)

func TestNewAdapterResourceUsesOnlyServiceName(t *testing.T) {
	resource := telemetry.NewAdapterResource()
	attributes := resource.Set().ToSlice()
	if len(attributes) != 1 {
		t.Fatalf("resource attributes = %#v, want exactly one", attributes)
	}
	if attributes[0].Key != semconv.ServiceNameKey || attributes[0].Value.AsString() != "blobfuse-health-exporter" {
		t.Fatalf("resource attributes = %#v, want only adapter service.name", attributes)
	}
	if resource.SchemaURL() != semconv.SchemaURL {
		t.Fatalf("SchemaURL() = %q, want %q", resource.SchemaURL(), semconv.SchemaURL)
	}
}

func TestNewBlobFuseResourceUsesExactPrivacyBoundedAttributes(t *testing.T) {
	identity := source.ProcessIdentity{
		BootID:       "4c1f5f44-7e22-4b5a-91d9-16d2d55f5c81",
		PID:          1234,
		StartTicks:   250,
		CreationTime: time.Date(2026, time.August, 20, 12, 34, 56, 789_000_000, time.UTC),
	}
	resource, err := telemetry.NewBlobFuseResource(identity)
	if err != nil {
		t.Fatalf("NewBlobFuseResource() error = %v", err)
	}

	attributes := resource.Set().ToSlice()
	if len(attributes) != 5 {
		t.Fatalf("resource attributes = %#v, want exactly 5", attributes)
	}
	want := map[attribute.Key]attribute.Value{
		semconv.ServiceNameKey:                          attribute.StringValue("blobfuse2"),
		semconv.ServiceInstanceIDKey:                    attribute.StringValue(identity.InstanceID()),
		semconv.ProcessPIDKey:                           attribute.IntValue(identity.PID),
		semconv.ProcessCreationTimeKey:                  attribute.StringValue("2026-08-20T12:34:56.789Z"),
		attribute.Key(telemetry.MonitorSourceAttribute): attribute.StringValue("bfusemon_json"),
	}
	for _, resourceAttribute := range attributes {
		wantValue, found := want[resourceAttribute.Key]
		if !found {
			t.Fatalf("unexpected resource attribute %q", resourceAttribute.Key)
		}
		if resourceAttribute.Value != wantValue {
			t.Fatalf("resource attribute %q = %v, want %v", resourceAttribute.Key, resourceAttribute.Value, wantValue)
		}
		delete(want, resourceAttribute.Key)
	}
	if len(want) != 0 {
		t.Fatalf("missing resource attributes: %#v", want)
	}
	if resource.SchemaURL() != semconv.SchemaURL {
		t.Fatalf("SchemaURL() = %q, want %q", resource.SchemaURL(), semconv.SchemaURL)
	}

	dump := fmt.Sprintf("%#v", attributes)
	for _, prohibited := range []string{
		identity.BootID,
		"/private/report/path",
		"account-name",
		"container-name",
	} {
		if strings.Contains(dump, prohibited) {
			t.Fatalf("resource retained prohibited value %q", prohibited)
		}
	}
}

func TestNewBlobFuseResourceRejectsIncompleteIdentity(t *testing.T) {
	identities := []source.ProcessIdentity{
		{},
		{BootID: "boot", PID: 1, CreationTime: time.Now()},
		{BootID: "boot", StartTicks: 1, CreationTime: time.Now()},
		{BootID: "boot", PID: 1, StartTicks: 1},
	}
	for _, identity := range identities {
		if _, err := telemetry.NewBlobFuseResource(identity); err == nil {
			t.Fatalf("NewBlobFuseResource(%#v) error = nil", identity)
		}
	}
}
