package config_test

import (
	"reflect"
	"testing"
	"time"

	"github.com/AaronWangTT/blobfuse-health-exporter/internal/config"
)

func TestDefaultMatchesVersionZeroContract(t *testing.T) {
	got := config.Default()
	want := config.Config{
		OTLPEndpoint:          "http://127.0.0.1:4318/v1/metrics",
		RescanInterval:        5 * time.Second,
		InitializationTimeout: 30 * time.Second,
		SourceDrainTimeout:    10 * time.Second,
		ExportInterval:        30 * time.Second,
		ExportTimeout:         10 * time.Second,
		ShutdownTimeout:       5 * time.Second,
		MaxRecordBytes:        16 * 1024 * 1024,
		LogLevel:              "info",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Default() = %#v, want %#v", got, want)
	}
}

func TestConfigValidateAcceptsContractBoundaries(t *testing.T) {
	for _, size := range []int{config.MinimumRecordBytes, config.MaximumRecordBytes} {
		candidate := validConfig()
		candidate.MaxRecordBytes = size
		if err := candidate.Validate(); err != nil {
			t.Fatalf("Validate(size %d) error = %v", size, err)
		}
	}
	for _, level := range []string{"debug", "info", "warn", "error"} {
		candidate := validConfig()
		candidate.LogLevel = level
		if err := candidate.Validate(); err != nil {
			t.Fatalf("Validate(level %q) error = %v", level, err)
		}
	}
}

func TestConfigValidateRejectsInvalidValues(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*config.Config)
	}{
		{name: "missing report directory", mutate: func(value *config.Config) { value.ReportDirectory = "" }},
		{name: "relative report directory", mutate: func(value *config.Config) { value.ReportDirectory = "reports" }},
		{name: "missing PID", mutate: func(value *config.Config) { value.PID = 0 }},
		{name: "negative PID", mutate: func(value *config.Config) { value.PID = -1 }},
		{name: "unsafe endpoint", mutate: func(value *config.Config) { value.OTLPEndpoint = "http://example.com/v1/metrics" }},
		{name: "rescan interval", mutate: func(value *config.Config) { value.RescanInterval = 0 }},
		{name: "initialization timeout", mutate: func(value *config.Config) { value.InitializationTimeout = 0 }},
		{name: "source drain timeout", mutate: func(value *config.Config) { value.SourceDrainTimeout = 0 }},
		{name: "export interval", mutate: func(value *config.Config) { value.ExportInterval = 0 }},
		{name: "export timeout", mutate: func(value *config.Config) { value.ExportTimeout = 0 }},
		{name: "shutdown timeout", mutate: func(value *config.Config) { value.ShutdownTimeout = 0 }},
		{name: "equal export timeout", mutate: func(value *config.Config) { value.ExportTimeout = value.ExportInterval }},
		{name: "larger export timeout", mutate: func(value *config.Config) { value.ExportTimeout = value.ExportInterval + time.Nanosecond }},
		{name: "record size below minimum", mutate: func(value *config.Config) { value.MaxRecordBytes = config.MinimumRecordBytes - 1 }},
		{name: "record size above maximum", mutate: func(value *config.Config) { value.MaxRecordBytes = config.MaximumRecordBytes + 1 }},
		{name: "log level", mutate: func(value *config.Config) { value.LogLevel = "verbose" }},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			candidate := validConfig()
			test.mutate(&candidate)
			if err := candidate.Validate(); err == nil {
				t.Fatal("Validate() error = nil")
			}
		})
	}
}

func TestConfigEndpointURLReturnsValidatedURL(t *testing.T) {
	candidate := validConfig()
	endpoint, err := candidate.EndpointURL()
	if err != nil {
		t.Fatalf("EndpointURL() error = %v", err)
	}
	if endpoint.Hostname() != "127.0.0.1" || endpoint.Path != "/v1/metrics" {
		t.Fatalf("EndpointURL() = %#v", endpoint)
	}
}

func validConfig() config.Config {
	value := config.Default()
	value.ReportDirectory = "/var/lib/blobfuse-health"
	value.PID = 1234
	return value
}
