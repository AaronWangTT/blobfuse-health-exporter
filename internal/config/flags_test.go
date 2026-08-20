package config_test

import (
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/AaronWangTT/blobfuse-health-exporter/internal/config"
)

func TestParseUsesDefaultsForOptionalFlags(t *testing.T) {
	got, err := config.Parse([]string{"--report-dir=/reports", "--pid=1234"})
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}
	want := config.Default()
	want.ReportDirectory = "/reports"
	want.PID = 1234
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Parse() = %#v, want %#v", got, want)
	}
}

func TestParseOverridesEveryOption(t *testing.T) {
	got, err := config.Parse([]string{
		"--report-dir=/reports",
		"--pid=4321",
		"--otlp-endpoint=http://[::1]:4318/custom/v1/metrics",
		"--rescan-interval=2s",
		"--initialization-timeout=20s",
		"--source-drain-timeout=7s",
		"--export-interval=1m",
		"--export-timeout=12s",
		"--shutdown-timeout=3s",
		"--max-record-bytes=1048576",
		"--allow-insecure-source",
		"--log-level=debug",
	})
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}
	want := config.Config{
		ReportDirectory:       "/reports",
		PID:                   4321,
		OTLPEndpoint:          "http://[::1]:4318/custom/v1/metrics",
		RescanInterval:        2 * time.Second,
		InitializationTimeout: 20 * time.Second,
		SourceDrainTimeout:    7 * time.Second,
		ExportInterval:        time.Minute,
		ExportTimeout:         12 * time.Second,
		ShutdownTimeout:       3 * time.Second,
		MaxRecordBytes:        config.MinimumRecordBytes,
		AllowInsecureSource:   true,
		LogLevel:              "debug",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Parse() = %#v, want %#v", got, want)
	}
}

func TestParseRejectsInvalidCommandLine(t *testing.T) {
	tests := [][]string{
		{"--pid=1234"},
		{"--report-dir=/reports"},
		{"--report-dir=/reports", "--pid=1234", "--unknown=true"},
		{"--report-dir=/reports", "--pid=1234", "positional"},
		{"--report-dir=/reports", "--pid=1234", "--rescan-interval=invalid"},
		{"--report-dir=/reports", "--pid=1234", "--max-record-bytes=invalid"},
	}
	for _, args := range tests {
		if _, err := config.Parse(args); err == nil {
			t.Fatalf("Parse(%v) error = nil", args)
		}
	}
}

func TestParseReturnsTypedHelp(t *testing.T) {
	if _, err := config.Parse([]string{"--help"}); !errors.Is(err, config.ErrHelp) {
		t.Fatalf("Parse(--help) error = %v, want ErrHelp", err)
	}
}

func TestParseIgnoresOpenTelemetryEnvironmentConfiguration(t *testing.T) {
	t.Setenv("OTEL_EXPORTER_OTLP_METRICS_ENDPOINT", "https://example.com/secret")
	t.Setenv("OTEL_EXPORTER_OTLP_METRICS_TEMPORALITY_PREFERENCE", "delta")
	t.Setenv("OTEL_METRIC_EXPORT_INTERVAL", "1")
	t.Setenv("OTEL_METRIC_EXPORT_TIMEOUT", "1")

	got, err := config.Parse([]string{"--report-dir=/reports", "--pid=1234"})
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}
	defaults := config.Default()
	if got.OTLPEndpoint != defaults.OTLPEndpoint ||
		got.ExportInterval != defaults.ExportInterval ||
		got.ExportTimeout != defaults.ExportTimeout {
		t.Fatalf("environment changed command contract: %#v", got)
	}
}
