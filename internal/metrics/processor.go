package metrics

import (
	"fmt"
	"sync"

	"github.com/AaronWangTT/blobfuse-health-exporter/internal/source"
)

type RecordOutcome uint8

const (
	RecordAccepted RecordOutcome = iota + 1
	RecordIgnored
	RecordInvalid
)

type ProcessedRecord struct {
	Outcome       RecordOutcome
	IgnoredValues int
	InvalidValues int
	Metrics       ApplyResult
}

type ProcessingStats struct {
	AcceptedRecords uint64
	IgnoredRecords  uint64
	InvalidRecords  uint64
	IgnoredValues   uint64
	InvalidValues   uint64
	CounterResets   uint64
}

type RecordObserver func(ProcessedRecord)

// Processor normalizes source records and applies them to metric state before
// returning to the source watermark owner. Observer callbacks cannot fail.
type Processor struct {
	state    *State
	observer RecordObserver
	mutex    sync.RWMutex
	stats    ProcessingStats
}

func NewProcessor(state *State, observer RecordObserver) (*Processor, error) {
	if state == nil {
		return nil, fmt.Errorf("metric state is required")
	}
	return &Processor{state: state, observer: observer}, nil
}

func (processor *Processor) Process(mode source.RecordMode, record source.Record) error {
	normalized, err := source.NormalizeRecord(record)
	if err != nil {
		processed := ProcessedRecord{
			Outcome:       RecordInvalid,
			InvalidValues: 1,
		}
		processor.recordStats(processed)
		processor.notify(processed)
		return err
	}

	processed := ProcessedRecord{
		IgnoredValues: normalized.IgnoredValues,
		InvalidValues: normalized.InvalidValues,
		Metrics:       processor.state.Apply(mode, normalized),
	}
	metricValues := len(normalized.Counters) + len(normalized.Gauges)
	switch {
	case metricValues > 0:
		processed.Outcome = RecordAccepted
	case normalized.InvalidValues > 0:
		processed.Outcome = RecordInvalid
	default:
		processed.Outcome = RecordIgnored
	}

	processor.recordStats(processed)
	processor.notify(processed)
	return nil
}

func (processor *Processor) notify(processed ProcessedRecord) {
	processor.mutex.RLock()
	observer := processor.observer
	processor.mutex.RUnlock()
	if observer != nil {
		observer(processed)
	}
}

func (processor *Processor) Handler() source.RecordHandler {
	return processor.Process
}

func (processor *Processor) Stats() ProcessingStats {
	processor.mutex.RLock()
	defer processor.mutex.RUnlock()
	return processor.stats
}

func (processor *Processor) State() *State {
	if processor == nil {
		return nil
	}
	return processor.state
}

// AttachObserver connects transport recording after baseline cutover. Existing
// baseline state is not replayed, and the observer cannot be replaced.
func (processor *Processor) AttachObserver(observer RecordObserver) error {
	if observer == nil {
		return fmt.Errorf("record observer is required")
	}
	processor.mutex.Lock()
	defer processor.mutex.Unlock()
	if processor.observer != nil {
		return fmt.Errorf("record observer is already attached")
	}
	processor.observer = observer
	return nil
}

func (processor *Processor) recordStats(processed ProcessedRecord) {
	processor.mutex.Lock()
	defer processor.mutex.Unlock()
	switch processed.Outcome {
	case RecordAccepted:
		processor.stats.AcceptedRecords++
	case RecordIgnored:
		processor.stats.IgnoredRecords++
	case RecordInvalid:
		processor.stats.InvalidRecords++
	}
	processor.stats.IgnoredValues += uint64(processed.IgnoredValues)
	processor.stats.InvalidValues += uint64(processed.InvalidValues)
	processor.stats.CounterResets += uint64(len(processed.Metrics.CounterResets))
}
