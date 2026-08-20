//go:build linux

package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/AaronWangTT/blobfuse-health-exporter/internal/app"
	"github.com/AaronWangTT/blobfuse-health-exporter/internal/config"
)

var version = "dev"

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	os.Exit(run(ctx, os.Args[1:], os.Stdout, os.Stderr))
}

func run(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	settings, err := config.Parse(args)
	if errors.Is(err, config.ErrHelp) {
		fmt.Fprint(stdout, usage)
		return 0
	}
	if err != nil {
		fmt.Fprintf(stderr, "configuration error: %v\n", err)
		return 2
	}

	logger := slog.New(slog.NewTextHandler(stderr, &slog.HandlerOptions{
		Level: logLevel(settings.LogLevel),
	}))
	result, err := app.Run(ctx, settings, version)
	if err != nil {
		logger.Error("exporter stopped with an error", "error", err)
		return 1
	}
	if result.SourceEnded {
		logger.Info(
			"source process session ended",
			"unclean_generations",
			result.UncleanGenerations,
		)
	}
	return 0
}

func logLevel(value string) slog.Level {
	switch value {
	case "debug":
		return slog.LevelDebug
	case "warn":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}

const usage = `BlobFuse Health Exporter

Usage:
  blobfuse-health-exporter --report-dir PATH --pid PID [options]

Required:
  --report-dir PATH             Dedicated bfusemon report directory
  --pid PID                     Monitored BlobFuse process ID

Options:
  --otlp-endpoint URL           OTLP/HTTP metrics URL (default http://127.0.0.1:4318/v1/metrics)
  --rescan-interval DURATION    Directory and identity rescan interval (default 5s)
  --initialization-timeout DURATION
                                Baseline initialization timeout (default 30s)
  --source-drain-timeout DURATION
                                Source shutdown drain timeout (default 10s)
  --export-interval DURATION    Metric export interval (default 30s)
  --export-timeout DURATION     Metric export timeout (default 10s)
  --shutdown-timeout DURATION   Final flush and shutdown timeout (default 5s)
  --max-record-bytes BYTES      Maximum encoded source record (default 16777216)
  --allow-insecure-source       Relax source owner and mode checks for development
  --log-level LEVEL             debug, info, warn, or error (default info)
  --help                        Show this help
`
