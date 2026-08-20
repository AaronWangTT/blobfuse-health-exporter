package source_test

import (
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/AaronWangTT/blobfuse-health-exporter/internal/source"
)

func TestNormalizeRecordMapsAllowlistedAggregates(t *testing.T) {
	normalized := normalizeFixture(t, "aggregate.json")

	wantTimestamp := time.Date(2026, time.August, 20, 12, 0, 0, 0, time.UTC)
	if !normalized.HasTimestamp || !normalized.Timestamp.Equal(wantTimestamp) {
		t.Fatalf("timestamp = %v (present %t), want %v", normalized.Timestamp, normalized.HasTimestamp, wantTimestamp)
	}
	if normalized.InvalidValues != 0 {
		t.Fatalf("InvalidValues = %d, want 0", normalized.InvalidValues)
	}
	if normalized.IgnoredValues == 0 {
		t.Fatal("IgnoredValues = 0, want unsupported input to be counted")
	}

	wantCounters := map[source.Series]uint64{
		{Metric: source.MetricStorageIO, Direction: source.DirectionRead}:          1 << 53,
		{Metric: source.MetricStorageIO, Direction: source.DirectionWrite}:         42,
		{Metric: source.MetricFSOperations, Operation: source.OperationCreateFile}: 8,
		{Metric: source.MetricFSOperations, Operation: source.OperationDeleteFile}: 2,
		{Metric: source.MetricCacheDownloads}:                                      4,
		{Metric: source.MetricCacheHits}:                                           3,
	}
	if got := counterMap(normalized.Counters); !equalCounterMaps(got, wantCounters) {
		t.Fatalf("counters = %#v, want %#v", got, wantCounters)
	}

	wantGauges := map[source.Series]float64{
		{Metric: source.MetricOpenFiles, Component: source.ComponentAzStorage}: 2,
		{Metric: source.MetricOpenFiles, Component: source.ComponentLibfuse}:   3,
		{Metric: source.MetricCacheUsage}:                                      12.5 * 1024 * 1024,
		{Metric: source.MetricCacheUtilization}:                                1.25,
		{Metric: source.MetricMemoryVirtual}:                                   1234 * 1024 * 1024,
	}
	if got := gaugeMap(normalized.Gauges); !equalGaugeMaps(got, wantGauges) {
		t.Fatalf("gauges = %#v, want %#v", got, wantGauges)
	}
}

func TestNormalizeRecordDoesNotRetainSensitiveOrArbitraryValues(t *testing.T) {
	normalized := normalizeFixture(t, "aggregate.json")
	dump := fmt.Sprintf("%#v", normalized)

	for _, prohibited := range []string{
		"private/container/blob.txt",
		"private/cache/blob.txt",
		"secret-source-key",
		"Arbitrary Counter",
	} {
		if strings.Contains(dump, prohibited) {
			t.Fatalf("normalized record retained prohibited value %q", prohibited)
		}
	}
}

func TestNormalizeRecordOmitsInvalidFieldsIndependently(t *testing.T) {
	normalized := normalizeFixture(t, "invalid-values.json")

	if normalized.HasTimestamp {
		t.Fatalf("HasTimestamp = true, want false for an invalid timestamp")
	}
	if normalized.InvalidValues < 10 {
		t.Fatalf("InvalidValues = %d, want malformed known fields to be counted", normalized.InvalidValues)
	}
	if len(normalized.Gauges) != 0 {
		t.Fatalf("gauges = %#v, want every malformed gauge omitted", normalized.Gauges)
	}

	wantCounters := map[source.Series]uint64{
		{Metric: source.MetricFSOperations, Operation: source.OperationDeleteFile}: 2,
		{Metric: source.MetricCacheDownloads}:                                      1000,
		{Metric: source.MetricCacheHits}:                                           1 << 53,
	}
	if got := counterMap(normalized.Counters); !equalCounterMaps(got, wantCounters) {
		t.Fatalf("counters = %#v, want %#v", got, wantCounters)
	}

	dump := fmt.Sprintf("%#v", normalized)
	if strings.Contains(dump, "another/private/path") {
		t.Fatal("normalized record retained a value from an unknown component")
	}
}

func TestNormalizeRecordRejectsNonObject(t *testing.T) {
	_, err := source.NormalizeRecord(source.Record{Raw: []byte(`[]`)})
	if err == nil {
		t.Fatal("NormalizeRecord() error = nil, want non-object rejection")
	}
}

func TestNormalizeRecordIgnoresMissingOptionalMessageFields(t *testing.T) {
	normalized, err := source.NormalizeRecord(source.Record{Raw: []byte(`{"BlobfuseStats":[{}]}`)})
	if err != nil {
		t.Fatalf("NormalizeRecord() error = %v", err)
	}
	if normalized.InvalidValues != 0 {
		t.Fatalf("InvalidValues = %d, want 0", normalized.InvalidValues)
	}
	if normalized.IgnoredValues != 1 {
		t.Fatalf("IgnoredValues = %d, want 1", normalized.IgnoredValues)
	}
}

func TestNormalizeRecordRejectsIncompatibleEventValue(t *testing.T) {
	record := source.Record{Raw: []byte(`{
		"BlobfuseStats":[{
			"operation":"CreateFile",
			"path":"private/event/path",
			"value":42
		}]
	}`)}
	normalized, err := source.NormalizeRecord(record)
	if err != nil {
		t.Fatalf("NormalizeRecord() error = %v", err)
	}
	if normalized.InvalidValues != 1 {
		t.Fatalf("InvalidValues = %d, want 1", normalized.InvalidValues)
	}
	if strings.Contains(fmt.Sprintf("%#v", normalized), "private/event/path") {
		t.Fatal("normalized record retained an immediate-event path")
	}
}

func TestNormalizeRecordParsesSupportedTopMemorySuffixes(t *testing.T) {
	tests := []struct {
		value string
		want  float64
	}{
		{value: "1k", want: 1 << 10},
		{value: "1K", want: 1 << 10},
		{value: "1.5m", want: 1.5 * (1 << 20)},
		{value: "1.5M", want: 1.5 * (1 << 20)},
		{value: "2g", want: 2 * (1 << 30)},
		{value: "2G", want: 2 * (1 << 30)},
		{value: "3t", want: 3 * (1 << 40)},
		{value: "3T", want: 3 * (1 << 40)},
		{value: "4p", want: 4 * (1 << 50)},
		{value: "4P", want: 4 * (1 << 50)},
		{value: "5e", want: 5 * (1 << 60)},
		{value: "5E", want: 5 * (1 << 60)},
		{value: " 6.25m ", want: 6.25 * (1 << 20)},
		{value: "1 m", want: 1 << 20},
	}

	for _, test := range tests {
		t.Run(test.value, func(t *testing.T) {
			normalized := normalizeMemoryValue(t, test.value)
			if normalized.InvalidValues != 0 {
				t.Fatalf("InvalidValues = %d, want 0", normalized.InvalidValues)
			}
			got, found := gaugeMap(normalized.Gauges)[source.Series{Metric: source.MetricMemoryVirtual}]
			if !found || math.Abs(got-test.want) > 0.5 {
				t.Fatalf("memory gauge = %v (present %t), want %v", got, found, test.want)
			}
		})
	}
}

func TestNormalizeRecordRejectsUnsupportedTopMemoryValues(t *testing.T) {
	for _, value := range []string{
		"",
		"1",
		"1KB",
		"-1m",
		"NaNm",
		"Infm",
		"1z",
		"1e309m",
	} {
		t.Run(value, func(t *testing.T) {
			normalized := normalizeMemoryValue(t, value)
			if normalized.InvalidValues != 1 || len(normalized.Gauges) != 0 {
				t.Fatalf("normalized = %#v, want one invalid value and no gauge", normalized)
			}
		})
	}
}

func normalizeMemoryValue(t *testing.T, value string) source.NormalizedRecord {
	t.Helper()
	record := source.Record{Raw: []byte(fmt.Sprintf(`{"MemoryUsage":%q}`, value))}
	normalized, err := source.NormalizeRecord(record)
	if err != nil {
		t.Fatalf("NormalizeRecord() error = %v", err)
	}
	return normalized
}

func normalizeFixture(t *testing.T, name string) source.NormalizedRecord {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", "blobfuse-2.5.6", name))
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}

	decoder := newDecoder(t, len(data))
	appendAtReadOffset(t, decoder, data)
	record, err := decoder.Next()
	if err != nil {
		t.Fatalf("Next() error = %v", err)
	}
	normalized, err := source.NormalizeRecord(record)
	if err != nil {
		t.Fatalf("NormalizeRecord() error = %v", err)
	}
	if err := decoder.Commit(record); err != nil {
		t.Fatalf("Commit() error = %v", err)
	}
	return normalized
}

func counterMap(values []source.CounterValue) map[source.Series]uint64 {
	result := make(map[source.Series]uint64, len(values))
	for _, value := range values {
		result[value.Series] = value.Value
	}
	return result
}

func gaugeMap(values []source.GaugeValue) map[source.Series]float64 {
	result := make(map[source.Series]float64, len(values))
	for _, value := range values {
		result[value.Series] = value.Value
	}
	return result
}

func equalCounterMaps(left, right map[source.Series]uint64) bool {
	if len(left) != len(right) {
		return false
	}
	for series, value := range right {
		if left[series] != value {
			return false
		}
	}
	return true
}

func equalGaugeMaps(left, right map[source.Series]float64) bool {
	if len(left) != len(right) {
		return false
	}
	for series, value := range right {
		if left[series] != value {
			return false
		}
	}
	return true
}
