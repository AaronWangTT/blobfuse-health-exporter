//go:build linux

package source

import (
	"context"
	"fmt"
	"os"
)

type LiveResult struct {
	Records               int
	Bytes                 int
	NewGenerations        int
	NotificationOverflows int
}

// Reconcile rescans and drains every known generation from the current live
// watermark. Calls are serialized with Close and with other reconciliations.
func (session *SourceSession) Reconcile(ctx context.Context) (result LiveResult, resultErr error) {
	if session == nil {
		return LiveResult{}, os.ErrClosed
	}
	if ctx == nil {
		return LiveResult{}, fmt.Errorf("reconciliation context is required")
	}

	session.operationMutex.Lock()
	defer session.operationMutex.Unlock()
	session.stateMutex.RLock()
	closed := session.closed
	session.stateMutex.RUnlock()
	if closed {
		return LiveResult{}, os.ErrClosed
	}
	defer func() {
		session.notifyDiscontinuity(resultErr)
	}()

	for {
		if err := ctx.Err(); err != nil {
			return result, fmt.Errorf("reconcile report source: %w", err)
		}

		discovered, err := session.directory.ScanReports(session.identity)
		if err != nil {
			return result, err
		}
		registered, err := session.registry.register(discovered)
		if err != nil {
			return result, err
		}
		result.NewGenerations += registered
		if registered > 0 {
			session.notify(SourceEvent{Kind: SourceEventRotations, Count: registered})
		}

		for _, reader := range session.registry.oldestFirst() {
			drained, err := reader.drain(func(record Record) error {
				return session.handle(RecordModeLive, record)
			})
			if err != nil {
				return result, err
			}
			result.Records += drained.records
			result.Bytes += drained.bytes
		}

		events, err := session.watcher.Drain()
		if err != nil {
			return result, err
		}
		if events.Invalidated {
			return result, ErrWatcherInvalidated
		}
		if events.Overflowed {
			result.NotificationOverflows++
			session.stateMutex.Lock()
			session.notificationOverflows++
			session.stateMutex.Unlock()
		}
		if registered == 0 && !events.Changed {
			return result, nil
		}
	}
}

func (session *SourceSession) CurrentWatermarks() []GenerationWatermark {
	if session == nil {
		return nil
	}

	session.operationMutex.Lock()
	defer session.operationMutex.Unlock()
	session.stateMutex.RLock()
	closed := session.closed
	session.stateMutex.RUnlock()
	if closed {
		return nil
	}
	return session.registry.watermarks()
}
