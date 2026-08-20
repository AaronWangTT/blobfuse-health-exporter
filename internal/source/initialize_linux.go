//go:build linux

package source

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"
)

type RecordMode uint8

const (
	RecordModeBaseline RecordMode = iota + 1
	RecordModeLive
)

type RecordHandler func(RecordMode, Record) error

type InitializationOptions struct {
	MaxRecordBytes        int
	RescanInterval        time.Duration
	InitializationTimeout time.Duration
}

// SourceSession owns the watcher and all opened generation descriptors after
// baseline initialization. The caller continues to own the report directory.
type SourceSession struct {
	operationMutex        sync.Mutex
	stateMutex            sync.RWMutex
	directory             *ReportDirectory
	identity              ProcessIdentity
	watcher               *DirectoryWatcher
	registry              *generationRegistry
	handle                RecordHandler
	observer              SourceObserver
	cutover               []GenerationWatermark
	notificationOverflows uint64
	closed                bool
}

// InitializeSource establishes an oldest-to-newest baseline and atomically
// publishes its generation watermarks as the live observation boundary.
func InitializeSource(
	ctx context.Context,
	directory *ReportDirectory,
	identity ProcessIdentity,
	options InitializationOptions,
	handle RecordHandler,
) (*SourceSession, error) {
	if ctx == nil {
		return nil, fmt.Errorf("initialization context is required")
	}
	if directory == nil {
		return nil, fmt.Errorf("report directory is required")
	}
	if handle == nil {
		return nil, fmt.Errorf("record handler is required")
	}
	if options.RescanInterval <= 0 {
		return nil, fmt.Errorf("rescan interval must be positive")
	}
	if options.InitializationTimeout <= 0 {
		return nil, fmt.Errorf("initialization timeout must be positive")
	}

	registry, err := newGenerationRegistry(options.MaxRecordBytes)
	if err != nil {
		return nil, err
	}
	watcher, err := WatchReportDirectory(directory)
	if err != nil {
		registry.close()
		return nil, err
	}
	session := &SourceSession{
		directory: directory,
		identity:  identity,
		watcher:   watcher,
		registry:  registry,
		handle:    handle,
	}

	initializationContext, cancel := context.WithTimeout(ctx, options.InitializationTimeout)
	defer cancel()

	for {
		if err := initializationContext.Err(); err != nil {
			session.Close()
			return nil, fmt.Errorf("initialize report source: %w", err)
		}

		discovered, err := directory.ScanReports(identity)
		if err != nil {
			session.Close()
			return nil, err
		}
		registered, err := registry.register(discovered)
		if err != nil {
			session.Close()
			return nil, err
		}

		for _, reader := range registry.oldestFirst() {
			if _, err := reader.drain(func(record Record) error {
				return handle(RecordModeBaseline, record)
			}); err != nil {
				session.Close()
				return nil, err
			}
		}

		events, err := watcher.Drain()
		if err != nil {
			session.Close()
			return nil, err
		}
		if events.Invalidated {
			session.Close()
			return nil, ErrWatcherInvalidated
		}
		if events.Overflowed {
			session.notificationOverflows++
		}

		if len(registry.readers) > 0 && registered == 0 && !events.Changed {
			session.cutover = registry.watermarks()
			return session, nil
		}

		if registered > 0 || events.Changed {
			continue
		}
		if err := waitForRescan(initializationContext, options.RescanInterval); err != nil {
			session.Close()
			return nil, fmt.Errorf("initialize report source: %w", err)
		}
	}
}

func (session *SourceSession) Identity() ProcessIdentity {
	if session == nil {
		return ProcessIdentity{}
	}
	session.stateMutex.RLock()
	defer session.stateMutex.RUnlock()
	return session.identity
}

func (session *SourceSession) Cutover() []GenerationWatermark {
	if session == nil {
		return nil
	}
	session.stateMutex.RLock()
	defer session.stateMutex.RUnlock()
	return append([]GenerationWatermark(nil), session.cutover...)
}

func (session *SourceSession) NotificationOverflows() uint64 {
	if session == nil {
		return 0
	}
	session.stateMutex.RLock()
	defer session.stateMutex.RUnlock()
	return session.notificationOverflows
}

func (session *SourceSession) Close() error {
	if session == nil {
		return nil
	}

	session.operationMutex.Lock()
	defer session.operationMutex.Unlock()
	session.stateMutex.Lock()
	if session.closed {
		session.stateMutex.Unlock()
		return nil
	}
	session.closed = true
	session.stateMutex.Unlock()

	var closeErrors []error
	if session.watcher != nil {
		if err := session.watcher.Close(); err != nil {
			closeErrors = append(closeErrors, err)
		}
	}
	if session.registry != nil {
		if err := session.registry.close(); err != nil {
			closeErrors = append(closeErrors, err)
		}
	}
	return errors.Join(closeErrors...)
}

func waitForRescan(ctx context.Context, interval time.Duration) error {
	timer := time.NewTimer(interval)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
