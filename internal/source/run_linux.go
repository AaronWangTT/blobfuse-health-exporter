//go:build linux

package source

import (
	"context"
	"errors"
	"fmt"
	"time"
)

type RunOptions struct {
	RescanInterval     time.Duration
	SourceDrainTimeout time.Duration
}

type RunResult struct {
	Live            LiveResult
	Reconciliations int
	SourceEnd       error
	Drain           SourceDrainResult
}

// Run processes live source activity until the caller stops the session or
// the captured process identity ends. External cancellation skips source drain.
func (session *SourceSession) Run(ctx context.Context, options RunOptions) (RunResult, error) {
	return session.run(ctx, options, ReadProcessIdentity)
}

func (session *SourceSession) run(
	ctx context.Context,
	options RunOptions,
	lookup processIdentityLookup,
) (RunResult, error) {
	if session == nil {
		return RunResult{}, fmt.Errorf("source session is required")
	}
	if ctx == nil {
		return RunResult{}, fmt.Errorf("run context is required")
	}
	if options.RescanInterval <= 0 {
		return RunResult{}, fmt.Errorf("rescan interval must be positive")
	}
	if options.SourceDrainTimeout <= 0 {
		return RunResult{}, fmt.Errorf("source drain timeout must be positive")
	}

	var result RunResult
	for {
		_, err := session.watcher.Wait(ctx, options.RescanInterval)
		if err != nil {
			return result, err
		}

		identityErr := verifyProcessIdentity(session.Identity(), lookup)
		if identityErr != nil {
			if !errors.Is(identityErr, ErrSourceSessionEnded) {
				return result, identityErr
			}
			result.SourceEnd = identityErr
			drained, err := session.DrainSource(ctx, DrainOptions{
				Timeout:        options.SourceDrainTimeout,
				RescanInterval: options.RescanInterval,
			})
			result.Drain = drained
			return result, err
		}

		reconciled, err := session.Reconcile(ctx)
		mergeLiveResult(&result.Live, reconciled)
		if err != nil {
			return result, err
		}
		result.Reconciliations++
	}
}
