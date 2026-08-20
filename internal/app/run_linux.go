//go:build linux

package app

import (
	"context"
	"errors"
	"fmt"

	"github.com/AaronWangTT/blobfuse-health-exporter/internal/config"
	"github.com/AaronWangTT/blobfuse-health-exporter/internal/metrics"
	"github.com/AaronWangTT/blobfuse-health-exporter/internal/source"
	"github.com/AaronWangTT/blobfuse-health-exporter/internal/telemetry"
)

type Result struct {
	SourceEnded        bool
	UncleanGenerations int
}

func Run(ctx context.Context, settings config.Config, version string) (Result, error) {
	if ctx == nil {
		return Result{}, fmt.Errorf("application context is required")
	}
	if err := settings.Validate(); err != nil {
		return Result{}, err
	}

	identity, err := source.ReadProcessIdentity(settings.PID)
	if err != nil {
		return Result{}, err
	}
	directory, err := source.OpenReportDirectory(settings.ReportDirectory, settings.AllowInsecureSource)
	if err != nil {
		return Result{}, err
	}

	state := metrics.NewState()
	processor, err := metrics.NewProcessor(state, nil)
	if err != nil {
		directory.Close()
		return Result{}, err
	}
	session, err := source.InitializeSource(
		ctx,
		directory,
		identity,
		source.InitializationOptions{
			MaxRecordBytes:        settings.MaxRecordBytes,
			RescanInterval:        settings.RescanInterval,
			InitializationTimeout: settings.InitializationTimeout,
		},
		processor.Handler(),
	)
	if err != nil {
		directory.Close()
		return Result{}, err
	}

	pipeline, err := telemetry.NewPipeline(ctx, settings, identity, processor, version)
	if err != nil {
		session.Close()
		directory.Close()
		return Result{}, err
	}
	if err := session.AttachObserver(pipeline.RecordSourceEvent); err != nil {
		shutdownContext, cancel := context.WithTimeout(context.Background(), settings.ShutdownTimeout)
		shutdownErr := pipeline.Shutdown(shutdownContext)
		cancel()
		closeErr := errors.Join(session.Close(), directory.Close())
		return Result{}, errors.Join(err, shutdownErr, closeErr)
	}

	runResult, runErr := session.Run(ctx, source.RunOptions{
		RescanInterval:     settings.RescanInterval,
		SourceDrainTimeout: settings.SourceDrainTimeout,
	})
	result := Result{
		SourceEnded:        runResult.SourceEnd != nil,
		UncleanGenerations: len(runResult.Drain.UncleanGenerations),
	}
	if result.SourceEnded {
		state.ClearGauges()
	}
	if ctx.Err() != nil && errors.Is(runErr, ctx.Err()) {
		runErr = nil
	}

	shutdownContext, cancel := context.WithTimeout(context.Background(), settings.ShutdownTimeout)
	shutdownErr := pipeline.Shutdown(shutdownContext)
	cancel()
	closeErr := errors.Join(session.Close(), directory.Close())
	return result, errors.Join(runErr, shutdownErr, closeErr)
}
