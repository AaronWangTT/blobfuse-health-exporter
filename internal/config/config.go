package config

import (
	"fmt"
	"net/url"
	"path/filepath"
	"time"
)

const (
	MinimumRecordBytes = 1 * 1024 * 1024
	MaximumRecordBytes = 64 * 1024 * 1024
)

type Config struct {
	ReportDirectory       string
	PID                   int
	OTLPEndpoint          string
	RescanInterval        time.Duration
	InitializationTimeout time.Duration
	SourceDrainTimeout    time.Duration
	ExportInterval        time.Duration
	ExportTimeout         time.Duration
	ShutdownTimeout       time.Duration
	MaxRecordBytes        int
	AllowInsecureSource   bool
	LogLevel              string
}

func Default() Config {
	return Config{
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
}

func (config Config) Validate() error {
	if config.ReportDirectory == "" {
		return fmt.Errorf("report directory is required")
	}
	if !filepath.IsAbs(config.ReportDirectory) {
		return fmt.Errorf("report directory must be absolute")
	}
	if config.PID <= 0 {
		return fmt.Errorf("PID must be positive")
	}
	if _, err := ParseOTLPEndpoint(config.OTLPEndpoint); err != nil {
		return err
	}

	durations := []struct {
		name  string
		value time.Duration
	}{
		{name: "rescan interval", value: config.RescanInterval},
		{name: "initialization timeout", value: config.InitializationTimeout},
		{name: "source drain timeout", value: config.SourceDrainTimeout},
		{name: "export interval", value: config.ExportInterval},
		{name: "export timeout", value: config.ExportTimeout},
		{name: "shutdown timeout", value: config.ShutdownTimeout},
	}
	for _, duration := range durations {
		if duration.value <= 0 {
			return fmt.Errorf("%s must be positive", duration.name)
		}
	}
	if config.ExportTimeout >= config.ExportInterval {
		return fmt.Errorf("export timeout must be less than export interval")
	}
	if config.MaxRecordBytes < MinimumRecordBytes || config.MaxRecordBytes > MaximumRecordBytes {
		return fmt.Errorf("maximum record size must be between 1 MiB and 64 MiB")
	}
	if !validLogLevel(config.LogLevel) {
		return fmt.Errorf("log level must be debug, info, warn, or error")
	}
	return nil
}

func (config Config) EndpointURL() (*url.URL, error) {
	return ParseOTLPEndpoint(config.OTLPEndpoint)
}

func validLogLevel(value string) bool {
	switch value {
	case "debug", "info", "warn", "error":
		return true
	default:
		return false
	}
}
