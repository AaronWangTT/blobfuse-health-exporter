package config

import (
	"errors"
	"flag"
	"fmt"
	"io"
)

var ErrHelp = flag.ErrHelp

func Parse(args []string) (Config, error) {
	config := Default()
	flags := flag.NewFlagSet("blobfuse-health-exporter", flag.ContinueOnError)
	flags.SetOutput(io.Discard)

	flags.StringVar(&config.ReportDirectory, "report-dir", "", "dedicated bfusemon report directory")
	flags.IntVar(&config.PID, "pid", 0, "monitored BlobFuse process ID")
	flags.StringVar(&config.OTLPEndpoint, "otlp-endpoint", config.OTLPEndpoint, "OTLP/HTTP metrics URL")
	flags.DurationVar(&config.RescanInterval, "rescan-interval", config.RescanInterval, "source rescan interval")
	flags.DurationVar(&config.InitializationTimeout, "initialization-timeout", config.InitializationTimeout, "baseline initialization timeout")
	flags.DurationVar(&config.SourceDrainTimeout, "source-drain-timeout", config.SourceDrainTimeout, "source drain timeout")
	flags.DurationVar(&config.ExportInterval, "export-interval", config.ExportInterval, "metric export interval")
	flags.DurationVar(&config.ExportTimeout, "export-timeout", config.ExportTimeout, "metric export timeout")
	flags.DurationVar(&config.ShutdownTimeout, "shutdown-timeout", config.ShutdownTimeout, "final flush and shutdown timeout")
	flags.IntVar(&config.MaxRecordBytes, "max-record-bytes", config.MaxRecordBytes, "maximum encoded source record size")
	flags.BoolVar(&config.AllowInsecureSource, "allow-insecure-source", false, "relax source owner and mode checks")
	flags.StringVar(&config.LogLevel, "log-level", config.LogLevel, "log level")

	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return Config{}, ErrHelp
		}
		return Config{}, fmt.Errorf("parse command line: %w", err)
	}
	if flags.NArg() != 0 {
		return Config{}, fmt.Errorf("positional arguments are not supported")
	}
	if err := config.Validate(); err != nil {
		return Config{}, err
	}
	return config, nil
}
