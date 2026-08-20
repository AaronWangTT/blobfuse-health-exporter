package config_test

import (
	"strings"
	"testing"

	"github.com/AaronWangTT/blobfuse-health-exporter/internal/config"
)

func TestParseOTLPEndpointAcceptsLoopbackMetricsURLs(t *testing.T) {
	tests := []string{
		"http://127.0.0.1:4318/v1/metrics",
		"http://localhost:4318/v1/metrics",
		"http://LOCALHOST/custom/v1/metrics",
		"http://[::1]:4318/v1/metrics",
		"http://127.1.2.3/v1/metrics",
	}
	for _, value := range tests {
		t.Run(value, func(t *testing.T) {
			endpoint, err := config.ParseOTLPEndpoint(value)
			if err != nil {
				t.Fatalf("ParseOTLPEndpoint() error = %v", err)
			}
			if endpoint.String() != value {
				t.Fatalf("endpoint = %q, want %q", endpoint.String(), value)
			}
		})
	}
}

func TestParseOTLPEndpointRejectsUnsafeOrMalformedURLs(t *testing.T) {
	tests := []string{
		"",
		"127.0.0.1:4318/v1/metrics",
		"https://127.0.0.1:4318/v1/metrics",
		"http://example.com/v1/metrics",
		"http://localhost.example.com/v1/metrics",
		"http://0.0.0.0/v1/metrics",
		"http://user:secret@localhost/v1/metrics",
		"http://localhost/v1/metrics?sig=secret",
		"http://localhost/v1/metrics#fragment",
		"http://localhost/v1/traces",
		"http://localhost/v1/metrics/",
		"http:///v1/metrics",
		"http:localhost/v1/metrics",
		"http://[::1%25lo]:4318/v1/metrics",
		"http://localhost/%76%31%2fmetrics",
	}
	for _, value := range tests {
		t.Run(value, func(t *testing.T) {
			if _, err := config.ParseOTLPEndpoint(value); err == nil {
				t.Fatal("ParseOTLPEndpoint() error = nil")
			}
		})
	}
}

func TestParseOTLPEndpointErrorDoesNotEchoPotentialSecret(t *testing.T) {
	const secret = "do-not-log-this-secret"
	_, err := config.ParseOTLPEndpoint("http://user:" + secret + "@example.com/v1/metrics?sig=" + secret)
	if err == nil {
		t.Fatal("ParseOTLPEndpoint() error = nil")
	}
	if strings.Contains(err.Error(), secret) {
		t.Fatalf("error exposed endpoint secret: %v", err)
	}
}
