package telemetry

import (
	"github.com/AaronWangTT/blobfuse-health-exporter/internal/metrics"
	"github.com/AaronWangTT/blobfuse-health-exporter/internal/source"
)

func (pipeline *Pipeline) RecordSourceEvent(event source.SourceEvent) {
	if pipeline == nil || pipeline.selfRecorder == nil || event.Count <= 0 {
		return
	}
	switch event.Kind {
	case source.SourceEventRotations:
		pipeline.selfRecorder.RecordRotations(event.Count)
	case source.SourceEventDiscontinuity:
		reason, ok := selfDiscontinuityReason(event.Reason)
		if !ok {
			return
		}
		for range event.Count {
			pipeline.selfRecorder.RecordDiscontinuity(reason)
		}
	}
}

func selfDiscontinuityReason(reason source.SourceDiscontinuity) (metrics.DiscontinuityReason, bool) {
	switch reason {
	case source.SourceDiscontinuityGenerationMissing:
		return metrics.DiscontinuityGenerationMissing, true
	case source.SourceDiscontinuityGenerationTruncated:
		return metrics.DiscontinuityGenerationTruncated, true
	case source.SourceDiscontinuityOversizeRecord:
		return metrics.DiscontinuityOversizeRecord, true
	case source.SourceDiscontinuityStaleGeneration:
		return metrics.DiscontinuityStaleGeneration, true
	case source.SourceDiscontinuityUncleanClose:
		return metrics.DiscontinuityUncleanClose, true
	default:
		return "", false
	}
}
