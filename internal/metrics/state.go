package metrics

import (
	"sync"

	"github.com/AaronWangTT/blobfuse-health-exporter/internal/source"
)

type CounterIncrement struct {
	Series source.Series
	Delta  uint64
}

type CounterReset struct {
	Series   source.Series
	Previous uint64
	Current  uint64
}

type GaugeUpdate struct {
	Series source.Series
	Value  float64
}

type ApplyResult struct {
	CounterIncrements []CounterIncrement
	CounterResets     []CounterReset
	GaugeUpdates      []GaugeUpdate
}

// State translates process-lifetime source counters into adapter-run deltas
// and retains current gauges for exactly one source session.
type State struct {
	mutex            sync.RWMutex
	counterBaselines map[source.Series]uint64
	gauges           map[source.Series]float64
}

func NewState() *State {
	return &State{
		counterBaselines: make(map[source.Series]uint64),
		gauges:           make(map[source.Series]float64),
	}
}

func (state *State) Apply(mode source.RecordMode, record source.NormalizedRecord) ApplyResult {
	state.mutex.Lock()
	defer state.mutex.Unlock()

	var result ApplyResult
	for _, counter := range record.Counters {
		previous, observed := state.counterBaselines[counter.Series]
		state.counterBaselines[counter.Series] = counter.Value
		if mode == source.RecordModeBaseline || !observed || counter.Value == previous {
			continue
		}
		if counter.Value < previous {
			result.CounterResets = append(result.CounterResets, CounterReset{
				Series:   counter.Series,
				Previous: previous,
				Current:  counter.Value,
			})
			continue
		}
		result.CounterIncrements = append(result.CounterIncrements, CounterIncrement{
			Series: counter.Series,
			Delta:  counter.Value - previous,
		})
	}

	for _, gauge := range record.Gauges {
		state.gauges[gauge.Series] = gauge.Value
		result.GaugeUpdates = append(result.GaugeUpdates, GaugeUpdate{
			Series: gauge.Series,
			Value:  gauge.Value,
		})
	}
	return result
}

func (state *State) CounterBaselines() map[source.Series]uint64 {
	state.mutex.RLock()
	defer state.mutex.RUnlock()

	result := make(map[source.Series]uint64, len(state.counterBaselines))
	for series, value := range state.counterBaselines {
		result[series] = value
	}
	return result
}

func (state *State) Gauges() map[source.Series]float64 {
	state.mutex.RLock()
	defer state.mutex.RUnlock()

	result := make(map[source.Series]float64, len(state.gauges))
	for series, value := range state.gauges {
		result[series] = value
	}
	return result
}

// ClearGauges removes observations when the identified source session ends.
func (state *State) ClearGauges() []source.Series {
	state.mutex.Lock()
	defer state.mutex.Unlock()

	removed := make([]source.Series, 0, len(state.gauges))
	for series := range state.gauges {
		removed = append(removed, series)
		delete(state.gauges, series)
	}
	return removed
}
