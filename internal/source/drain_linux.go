//go:build linux

package source

import (
	"context"
	"errors"
	"fmt"
	"time"
)

type DrainOptions struct {
	Timeout        time.Duration
	RescanInterval time.Duration
}

type SourceDrainResult struct {
	LiveResult
	UncleanGenerations []GenerationWatermark
}

// DrainSource continues live reconciliation for a bounded source-shutdown
// window. It starts one final non-waiting reconciliation at the drain deadline
// before reporting any generation that lacks a clean closing bracket.
func (session *SourceSession) DrainSource(
	ctx context.Context,
	options DrainOptions,
) (SourceDrainResult, error) {
	if session == nil {
		return SourceDrainResult{}, fmt.Errorf("source session is required")
	}
	if ctx == nil {
		return SourceDrainResult{}, fmt.Errorf("drain context is required")
	}
	if options.Timeout <= 0 {
		return SourceDrainResult{}, fmt.Errorf("source drain timeout must be positive")
	}
	if options.RescanInterval <= 0 {
		return SourceDrainResult{}, fmt.Errorf("source drain rescan interval must be positive")
	}

	deadline := time.Now().Add(options.Timeout)
	var result SourceDrainResult
	for {
		reconciled, err := session.Reconcile(ctx)
		mergeLiveResult(&result.LiveResult, reconciled)
		if err != nil {
			return result, err
		}

		remaining := time.Until(deadline)
		if remaining <= 0 {
			break
		}
		wait := options.RescanInterval
		if remaining < wait {
			wait = remaining
		}
		if err := waitForRescan(ctx, wait); err != nil {
			return result, fmt.Errorf("drain report source: %w", err)
		}
	}

	finalResult, err := session.Reconcile(ctx)
	mergeLiveResult(&result.LiveResult, finalResult)
	if err != nil && !errors.Is(err, context.Canceled) {
		return result, err
	}

	for _, watermark := range session.CurrentWatermarks() {
		if !watermark.Closed {
			result.UncleanGenerations = append(result.UncleanGenerations, watermark)
		}
	}
	if len(result.UncleanGenerations) > 0 {
		session.notify(SourceEvent{
			Kind:   SourceEventDiscontinuity,
			Count:  len(result.UncleanGenerations),
			Reason: SourceDiscontinuityUncleanClose,
		})
	}
	return result, err
}

func mergeLiveResult(target *LiveResult, addition LiveResult) {
	target.Records += addition.Records
	target.Bytes += addition.Bytes
	target.NewGenerations += addition.NewGenerations
	target.NotificationOverflows += addition.NotificationOverflows
}
