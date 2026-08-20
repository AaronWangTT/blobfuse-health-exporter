package metrics_test

import (
	"strings"
	"testing"

	"github.com/AaronWangTT/blobfuse-health-exporter/internal/metrics"
	"github.com/AaronWangTT/blobfuse-health-exporter/internal/source"
)

func TestDescribeSeriesUsesOnlyContractAttributes(t *testing.T) {
	tests := []struct {
		name       string
		series     source.Series
		metricName string
		kind       metrics.InstrumentKind
		attributes []metrics.Attribute
	}{
		{
			name:       "storage read",
			series:     readSeries,
			metricName: "azure.blobfuse.storage.io",
			kind:       metrics.InstrumentCounter,
			attributes: []metrics.Attribute{{Key: metrics.AttributeIODirection, Value: "read"}},
		},
		{
			name: "filesystem operation",
			series: source.Series{
				Metric:    source.MetricFSOperations,
				Operation: source.OperationRenameFile,
			},
			metricName: "azure.blobfuse.fs.operations",
			kind:       metrics.InstrumentCounter,
			attributes: []metrics.Attribute{{Key: metrics.AttributeOperationName, Value: "rename_file"}},
		},
		{
			name:       "open files",
			series:     openFilesSeries,
			metricName: "azure.blobfuse.file.open",
			kind:       metrics.InstrumentGauge,
			attributes: []metrics.Attribute{{Key: metrics.AttributeComponentName, Value: "libfuse"}},
		},
		{
			name:       "cache hits",
			series:     source.Series{Metric: source.MetricCacheHits},
			metricName: "azure.blobfuse.cache.hits",
			kind:       metrics.InstrumentCounter,
		},
		{
			name:       "virtual memory",
			series:     source.Series{Metric: source.MetricMemoryVirtual},
			metricName: "process.memory.virtual",
			kind:       metrics.InstrumentObservableUpDownCounter,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			descriptor, attributes, err := metrics.DescribeSeries(test.series)
			if err != nil {
				t.Fatalf("DescribeSeries() error = %v", err)
			}
			if descriptor.Name != test.metricName || descriptor.Kind != test.kind {
				t.Fatalf("descriptor = %#v, want name %q and kind %d", descriptor, test.metricName, test.kind)
			}
			if len(attributes) != len(test.attributes) {
				t.Fatalf("attributes = %#v, want %#v", attributes, test.attributes)
			}
			for index := range attributes {
				if attributes[index] != test.attributes[index] {
					t.Fatalf("attributes = %#v, want %#v", attributes, test.attributes)
				}
			}
		})
	}
}

func TestDescribeSeriesMapsEveryAllowedOperation(t *testing.T) {
	operations := []source.Operation{
		source.OperationCreateDir,
		source.OperationDeleteDir,
		source.OperationStreamDir,
		source.OperationRenameDir,
		source.OperationCreateFile,
		source.OperationDeleteFile,
		source.OperationRenameFile,
		source.OperationTruncateFile,
		source.OperationCreateLink,
		source.OperationReadLink,
		source.OperationSyncFile,
		source.OperationSyncDir,
		source.OperationChmod,
	}
	seen := make(map[string]bool, len(operations))
	for _, operation := range operations {
		_, attributes, err := metrics.DescribeSeries(source.Series{
			Metric:    source.MetricFSOperations,
			Operation: operation,
		})
		if err != nil {
			t.Fatalf("DescribeSeries(operation %d) error = %v", operation, err)
		}
		value := attributes[0].Value
		if seen[value] {
			t.Fatalf("operation attribute %q is duplicated", value)
		}
		seen[value] = true
	}
	if len(seen) != 13 {
		t.Fatalf("operation timeseries = %d, want 13", len(seen))
	}
}

func TestDescribeSeriesRejectsInvalidCombinations(t *testing.T) {
	tests := []source.Series{
		{},
		{Metric: source.MetricStorageIO},
		{Metric: source.MetricStorageIO, Direction: source.Direction(99)},
		{Metric: source.MetricFSOperations, Operation: source.Operation(99)},
		{Metric: source.MetricOpenFiles, Component: source.Component(99)},
		{Metric: source.MetricCacheUsage, Direction: source.DirectionRead},
	}
	for _, series := range tests {
		if _, _, err := metrics.DescribeSeries(series); err == nil {
			t.Fatalf("DescribeSeries(%#v) error = nil", series)
		}
	}
}

func TestDescribeSeriesCannotContainSourceText(t *testing.T) {
	descriptor, attributes, err := metrics.DescribeSeries(source.Series{
		Metric:    source.MetricFSOperations,
		Operation: source.OperationCreateFile,
	})
	if err != nil {
		t.Fatalf("DescribeSeries() error = %v", err)
	}
	dump := descriptor.Name + descriptor.Description + descriptor.Unit
	for _, attribute := range attributes {
		dump += attribute.Key + attribute.Value
	}
	for _, prohibited := range []string{"private/path", "account-name", "container-name"} {
		if strings.Contains(dump, prohibited) {
			t.Fatalf("descriptor retained prohibited value %q", prohibited)
		}
	}
}
