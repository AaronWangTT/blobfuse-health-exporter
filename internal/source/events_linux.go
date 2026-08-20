//go:build linux

package source

import (
	"errors"
	"fmt"
	"os"
)

type SourceEventKind uint8

const (
	SourceEventRotations SourceEventKind = iota + 1
	SourceEventDiscontinuity
)

type SourceDiscontinuity uint8

const (
	SourceDiscontinuityGenerationMissing SourceDiscontinuity = iota + 1
	SourceDiscontinuityGenerationTruncated
	SourceDiscontinuityOversizeRecord
	SourceDiscontinuityStaleGeneration
	SourceDiscontinuityUncleanClose
)

type SourceEvent struct {
	Kind   SourceEventKind
	Count  int
	Reason SourceDiscontinuity
}

type SourceObserver func(SourceEvent)

func (session *SourceSession) AttachObserver(observer SourceObserver) error {
	if session == nil {
		return fmt.Errorf("source session is required")
	}
	if observer == nil {
		return fmt.Errorf("source observer is required")
	}

	session.stateMutex.Lock()
	defer session.stateMutex.Unlock()
	if session.closed {
		return os.ErrClosed
	}
	if session.observer != nil {
		return fmt.Errorf("source observer is already attached")
	}
	session.observer = observer
	return nil
}

func (session *SourceSession) notify(event SourceEvent) {
	if session == nil || event.Count <= 0 {
		return
	}
	session.stateMutex.RLock()
	observer := session.observer
	session.stateMutex.RUnlock()
	if observer != nil {
		observer(event)
	}
}

func (session *SourceSession) notifyDiscontinuity(err error) {
	reason, ok := discontinuityForError(err)
	if !ok {
		return
	}
	session.notify(SourceEvent{
		Kind:   SourceEventDiscontinuity,
		Count:  1,
		Reason: reason,
	})
}

func discontinuityForError(err error) (SourceDiscontinuity, bool) {
	switch {
	case errors.Is(err, ErrGenerationTruncated):
		return SourceDiscontinuityGenerationTruncated, true
	case errors.Is(err, ErrRecordTooLarge):
		return SourceDiscontinuityOversizeRecord, true
	case errors.Is(err, ErrStaleGeneration):
		return SourceDiscontinuityStaleGeneration, true
	default:
		return 0, false
	}
}
