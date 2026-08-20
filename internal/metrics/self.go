package metrics

import (
	"context"
	"fmt"

	"go.opentelemetry.io/otel/attribute"
	otelmetric "go.opentelemetry.io/otel/metric"
)

const (
	SelfAttributeOutcome      = "outcome"
	SelfAttributeReason       = "reason"
	SelfAttributeSourceMetric = "source.metric"
	SelfAttributeErrorType    = "error.type"
)

type DiscontinuityReason string

const (
	DiscontinuityGenerationMissing   DiscontinuityReason = "generation_missing"
	DiscontinuityGenerationTruncated DiscontinuityReason = "generation_truncated"
	DiscontinuityOversizeRecord      DiscontinuityReason = "oversize_record"
	DiscontinuityStaleGeneration     DiscontinuityReason = "stale_generation"
	DiscontinuityUncleanClose        DiscontinuityReason = "unclean_close"
)

type ExportErrorType string

const (
	ExportErrorTimeout   ExportErrorType = "timeout"
	ExportErrorTransport ExportErrorType = "transport"
	ExportErrorShutdown  ExportErrorType = "shutdown"
)

type SelfRecorder struct {
	sourceRecords         otelmetric.Int64Counter
	sourceRotations       otelmetric.Int64Counter
	sourceDiscontinuities otelmetric.Int64Counter
	sourceCounterResets   otelmetric.Int64Counter
	exportErrors          otelmetric.Int64Counter
}

func NewSelfRecorder(meter otelmetric.Meter) (*SelfRecorder, error) {
	if meter == nil {
		return nil, fmt.Errorf("OpenTelemetry meter is required")
	}

	sourceRecords, err := meter.Int64Counter(
		"blobfuse_health_exporter.source.records",
		otelmetric.WithDescription("Source records processed by the adapter"),
		otelmetric.WithUnit("{record}"),
	)
	if err != nil {
		return nil, err
	}
	sourceRotations, err := meter.Int64Counter(
		"blobfuse_health_exporter.source.rotations",
		otelmetric.WithDescription("Report generations discovered after live cutover"),
		otelmetric.WithUnit("{rotation}"),
	)
	if err != nil {
		return nil, err
	}
	sourceDiscontinuities, err := meter.Int64Counter(
		"blobfuse_health_exporter.source.discontinuities",
		otelmetric.WithDescription("Discontinuities in the report source"),
		otelmetric.WithUnit("{discontinuity}"),
	)
	if err != nil {
		return nil, err
	}
	sourceCounterResets, err := meter.Int64Counter(
		"blobfuse_health_exporter.source.counter_resets",
		otelmetric.WithDescription("Source counter decreases observed after live cutover"),
		otelmetric.WithUnit("{reset}"),
	)
	if err != nil {
		return nil, err
	}
	exportErrors, err := meter.Int64Counter(
		"blobfuse_health_exporter.export.errors",
		otelmetric.WithDescription("Metric export errors"),
		otelmetric.WithUnit("{error}"),
	)
	if err != nil {
		return nil, err
	}

	return &SelfRecorder{
		sourceRecords:         sourceRecords,
		sourceRotations:       sourceRotations,
		sourceDiscontinuities: sourceDiscontinuities,
		sourceCounterResets:   sourceCounterResets,
		exportErrors:          exportErrors,
	}, nil
}

func (recorder *SelfRecorder) RecordProcessingStats(stats ProcessingStats) {
	recorder.addRecordOutcome(RecordAccepted, stats.AcceptedRecords)
	recorder.addRecordOutcome(RecordIgnored, stats.IgnoredRecords)
	recorder.addRecordOutcome(RecordInvalid, stats.InvalidRecords)
}

func (recorder *SelfRecorder) Record(processed ProcessedRecord) {
	if recorder == nil {
		return
	}
	recorder.addRecordOutcome(processed.Outcome, 1)
	for _, reset := range processed.Metrics.CounterResets {
		descriptor, _, err := DescribeSeries(reset.Series)
		if err != nil || descriptor.Kind != InstrumentCounter {
			continue
		}
		recorder.sourceCounterResets.Add(
			context.Background(),
			1,
			otelmetric.WithAttributes(attribute.String(SelfAttributeSourceMetric, descriptor.Name)),
		)
	}
}

func (recorder *SelfRecorder) RecordRotations(count int) {
	if recorder == nil || count <= 0 {
		return
	}
	recorder.sourceRotations.Add(context.Background(), int64(count))
}

func (recorder *SelfRecorder) RecordDiscontinuity(reason DiscontinuityReason) {
	if recorder == nil || !validDiscontinuityReason(reason) {
		return
	}
	recorder.sourceDiscontinuities.Add(
		context.Background(),
		1,
		otelmetric.WithAttributes(attribute.String(SelfAttributeReason, string(reason))),
	)
}

func (recorder *SelfRecorder) RecordExportError(errorType ExportErrorType) {
	if recorder == nil || !validExportErrorType(errorType) {
		return
	}
	recorder.exportErrors.Add(
		context.Background(),
		1,
		otelmetric.WithAttributes(attribute.String(SelfAttributeErrorType, string(errorType))),
	)
}

func (recorder *SelfRecorder) addRecordOutcome(outcome RecordOutcome, count uint64) {
	if recorder == nil || count == 0 {
		return
	}
	name, ok := recordOutcomeName(outcome)
	if !ok {
		return
	}
	recorder.sourceRecords.Add(
		context.Background(),
		int64(count),
		otelmetric.WithAttributes(attribute.String(SelfAttributeOutcome, name)),
	)
}

func recordOutcomeName(outcome RecordOutcome) (string, bool) {
	switch outcome {
	case RecordAccepted:
		return "accepted", true
	case RecordIgnored:
		return "ignored", true
	case RecordInvalid:
		return "invalid", true
	default:
		return "", false
	}
}

func validDiscontinuityReason(reason DiscontinuityReason) bool {
	switch reason {
	case DiscontinuityGenerationMissing,
		DiscontinuityGenerationTruncated,
		DiscontinuityOversizeRecord,
		DiscontinuityStaleGeneration,
		DiscontinuityUncleanClose:
		return true
	default:
		return false
	}
}

func validExportErrorType(errorType ExportErrorType) bool {
	switch errorType {
	case ExportErrorTimeout, ExportErrorTransport, ExportErrorShutdown:
		return true
	default:
		return false
	}
}
