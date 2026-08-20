package metrics

import (
	"fmt"

	"github.com/AaronWangTT/blobfuse-health-exporter/internal/source"
)

const (
	AttributeComponentName = "azure.blobfuse.component.name"
	AttributeIODirection   = "azure.blobfuse.io.direction"
	AttributeOperationName = "azure.blobfuse.operation.name"
)

type InstrumentKind uint8

const (
	InstrumentCounter InstrumentKind = iota + 1
	InstrumentGauge
	InstrumentObservableUpDownCounter
)

type Descriptor struct {
	Name        string
	Description string
	Unit        string
	Kind        InstrumentKind
}

type Attribute struct {
	Key   string
	Value string
}

// DescribeSeries maps only contract-approved series to metric metadata and
// bounded attributes. No source string is accepted as input.
func DescribeSeries(series source.Series) (Descriptor, []Attribute, error) {
	switch series.Metric {
	case source.MetricStorageIO:
		if series.Component != 0 || series.Operation != 0 {
			return Descriptor{}, nil, invalidSeries(series)
		}
		direction, ok := directionName(series.Direction)
		if !ok {
			return Descriptor{}, nil, invalidSeries(series)
		}
		return Descriptor{
			Name:        "azure.blobfuse.storage.io",
			Description: "Bytes transferred between BlobFuse and Azure Storage",
			Unit:        "By",
			Kind:        InstrumentCounter,
		}, []Attribute{{Key: AttributeIODirection, Value: direction}}, nil

	case source.MetricFSOperations:
		if series.Component != 0 || series.Direction != 0 {
			return Descriptor{}, nil, invalidSeries(series)
		}
		operation, ok := operationName(series.Operation)
		if !ok {
			return Descriptor{}, nil, invalidSeries(series)
		}
		return Descriptor{
			Name:        "azure.blobfuse.fs.operations",
			Description: "Filesystem operations observed at BlobFuse's libfuse boundary",
			Unit:        "{operation}",
			Kind:        InstrumentCounter,
		}, []Attribute{{Key: AttributeOperationName, Value: operation}}, nil

	case source.MetricOpenFiles:
		if series.Direction != 0 || series.Operation != 0 {
			return Descriptor{}, nil, invalidSeries(series)
		}
		component, ok := componentName(series.Component)
		if !ok {
			return Descriptor{}, nil, invalidSeries(series)
		}
		return Descriptor{
			Name:        "azure.blobfuse.file.open",
			Description: "Current open file handles reported by a BlobFuse component",
			Unit:        "{file}",
			Kind:        InstrumentGauge,
		}, []Attribute{{Key: AttributeComponentName, Value: component}}, nil

	case source.MetricCacheDownloads:
		return descriptorWithoutAttributes(series, Descriptor{
			Name:        "azure.blobfuse.cache.file.downloads",
			Description: "Files downloaded into the whole-file cache",
			Unit:        "{file}",
			Kind:        InstrumentCounter,
		})

	case source.MetricCacheHits:
		return descriptorWithoutAttributes(series, Descriptor{
			Name:        "azure.blobfuse.cache.hits",
			Description: "Open requests served from the whole-file cache",
			Unit:        "{hit}",
			Kind:        InstrumentCounter,
		})

	case source.MetricCacheUsage:
		return descriptorWithoutAttributes(series, Descriptor{
			Name:        "azure.blobfuse.cache.usage",
			Description: "Current disk usage reported by the whole-file cache policy",
			Unit:        "By",
			Kind:        InstrumentGauge,
		})

	case source.MetricCacheUtilization:
		return descriptorWithoutAttributes(series, Descriptor{
			Name:        "azure.blobfuse.cache.utilization",
			Description: "Fraction of the configured whole-file cache limit in use",
			Unit:        "1",
			Kind:        InstrumentGauge,
		})

	case source.MetricMemoryVirtual:
		return descriptorWithoutAttributes(series, Descriptor{
			Name:        "process.memory.virtual",
			Description: "Committed virtual memory of the monitored BlobFuse process",
			Unit:        "By",
			Kind:        InstrumentObservableUpDownCounter,
		})

	default:
		return Descriptor{}, nil, invalidSeries(series)
	}
}

func descriptorWithoutAttributes(series source.Series, descriptor Descriptor) (Descriptor, []Attribute, error) {
	if series.Component != 0 || series.Direction != 0 || series.Operation != 0 {
		return Descriptor{}, nil, invalidSeries(series)
	}
	return descriptor, nil, nil
}

func directionName(direction source.Direction) (string, bool) {
	switch direction {
	case source.DirectionRead:
		return "read", true
	case source.DirectionWrite:
		return "write", true
	default:
		return "", false
	}
}

func componentName(component source.Component) (string, bool) {
	switch component {
	case source.ComponentLibfuse:
		return "libfuse", true
	case source.ComponentAzStorage:
		return "azstorage", true
	default:
		return "", false
	}
}

func operationName(operation source.Operation) (string, bool) {
	switch operation {
	case source.OperationCreateDir:
		return "create_dir", true
	case source.OperationDeleteDir:
		return "delete_dir", true
	case source.OperationStreamDir:
		return "stream_dir", true
	case source.OperationRenameDir:
		return "rename_dir", true
	case source.OperationCreateFile:
		return "create_file", true
	case source.OperationDeleteFile:
		return "delete_file", true
	case source.OperationRenameFile:
		return "rename_file", true
	case source.OperationTruncateFile:
		return "truncate_file", true
	case source.OperationCreateLink:
		return "create_link", true
	case source.OperationReadLink:
		return "read_link", true
	case source.OperationSyncFile:
		return "sync_file", true
	case source.OperationSyncDir:
		return "sync_dir", true
	case source.OperationChmod:
		return "chmod", true
	default:
		return "", false
	}
}

func invalidSeries(series source.Series) error {
	return fmt.Errorf("unsupported metric series: metric=%d component=%d direction=%d operation=%d",
		series.Metric,
		series.Component,
		series.Direction,
		series.Operation,
	)
}
