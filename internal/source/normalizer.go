package source

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"math/big"
	"strconv"
	"strings"
	"time"
)

type Metric uint8

const (
	MetricStorageIO Metric = iota + 1
	MetricFSOperations
	MetricOpenFiles
	MetricCacheDownloads
	MetricCacheHits
	MetricCacheUsage
	MetricCacheUtilization
	MetricMemoryVirtual
)

type Component uint8

const (
	ComponentLibfuse Component = iota + 1
	ComponentAzStorage
)

type Direction uint8

const (
	DirectionRead Direction = iota + 1
	DirectionWrite
)

type Operation uint8

const (
	OperationCreateDir Operation = iota + 1
	OperationDeleteDir
	OperationStreamDir
	OperationRenameDir
	OperationCreateFile
	OperationDeleteFile
	OperationRenameFile
	OperationTruncateFile
	OperationCreateLink
	OperationReadLink
	OperationSyncFile
	OperationSyncDir
	OperationChmod
)

type Series struct {
	Metric    Metric
	Component Component
	Direction Direction
	Operation Operation
}

type CounterValue struct {
	Series Series
	Value  uint64
}

type GaugeValue struct {
	Series Series
	Value  float64
}

type NormalizedRecord struct {
	Timestamp     time.Time
	HasTimestamp  bool
	Counters      []CounterValue
	Gauges        []GaugeValue
	IgnoredValues int
	InvalidValues int
}

type operationField struct {
	sourceName string
	operation  Operation
}

var operationFields = []operationField{
	{sourceName: "CreateDir", operation: OperationCreateDir},
	{sourceName: "DeleteDir", operation: OperationDeleteDir},
	{sourceName: "StreamDir", operation: OperationStreamDir},
	{sourceName: "RenameDir", operation: OperationRenameDir},
	{sourceName: "CreateFile", operation: OperationCreateFile},
	{sourceName: "DeleteFile", operation: OperationDeleteFile},
	{sourceName: "RenameFile", operation: OperationRenameFile},
	{sourceName: "TruncateFile", operation: OperationTruncateFile},
	{sourceName: "CreateLink", operation: OperationCreateLink},
	{sourceName: "ReadLink", operation: OperationReadLink},
	{sourceName: "SyncFile", operation: OperationSyncFile},
	{sourceName: "SyncDir", operation: OperationSyncDir},
	{sourceName: "Chmod", operation: OperationChmod},
}

var maxExactSourceInteger = new(big.Int).Lsh(big.NewInt(1), 53)

func NormalizeRecord(record Record) (NormalizedRecord, error) {
	var fields map[string]json.RawMessage
	if err := record.Decode(&fields); err != nil {
		return NormalizedRecord{}, fmt.Errorf("normalize source record: %w", err)
	}
	if fields == nil {
		return NormalizedRecord{}, fmt.Errorf("normalize source record: expected object")
	}

	var normalized NormalizedRecord
	for field := range fields {
		if !isKnownTopLevelField(field) {
			normalized.IgnoredValues++
		}
	}

	normalizeTimestamp(fields["Timestamp"], &normalized)
	normalizeBlobfuseStats(fields["BlobfuseStats"], &normalized)
	normalizeUnsupportedArray(fields["FileCache"], &normalized)
	normalizeUnsupportedString(fields["CPUUsage"], &normalized)
	normalizeMemory(fields["MemoryUsage"], &normalized)
	normalizeUnsupportedString(fields["NetworkUsage"], &normalized)

	return normalized, nil
}

func normalizeTimestamp(raw json.RawMessage, normalized *NormalizedRecord) {
	if raw == nil {
		return
	}

	var value string
	if !decodeSourceValue(raw, &value) {
		normalized.InvalidValues++
		return
	}
	timestamp, err := time.Parse(time.RFC3339, value)
	if err != nil {
		normalized.InvalidValues++
		return
	}

	normalized.Timestamp = timestamp
	normalized.HasTimestamp = true
}

func normalizeBlobfuseStats(raw json.RawMessage, normalized *NormalizedRecord) {
	if raw == nil {
		return
	}

	var messages []json.RawMessage
	if !decodeSourceValue(raw, &messages) {
		normalized.InvalidValues++
		return
	}

	for _, message := range messages {
		normalizeBlobfuseMessage(message, normalized)
	}
}

func normalizeBlobfuseMessage(raw json.RawMessage, normalized *NormalizedRecord) {
	var fields map[string]json.RawMessage
	if !decodeSourceValue(raw, &fields) || fields == nil {
		normalized.InvalidValues++
		return
	}

	for field := range fields {
		if !isKnownMessageField(field) {
			normalized.IgnoredValues++
		}
	}

	if fields["operation"] != nil || fields["path"] != nil {
		validateEventFields(fields, normalized)
		normalized.IgnoredValues++
		return
	}

	validateMessageTimestamp(fields["timestamp"], normalized)

	componentRaw, found := fields["componentName"]
	if !found {
		normalized.IgnoredValues++
		return
	}
	var component string
	if !decodeSourceValue(componentRaw, &component) {
		normalized.InvalidValues++
		return
	}

	valuesRaw, found := fields["value"]
	if !found {
		normalized.IgnoredValues++
		return
	}
	var values map[string]json.RawMessage
	if !decodeSourceValue(valuesRaw, &values) || values == nil {
		normalized.InvalidValues++
		return
	}

	switch component {
	case "azstorage":
		normalizeAzStorage(values, normalized)
	case "libfuse":
		normalizeLibfuse(values, normalized)
	case "file_cache":
		normalizeFileCache(values, normalized)
	default:
		normalized.IgnoredValues++
	}
}

func normalizeAzStorage(values map[string]json.RawMessage, normalized *NormalizedRecord) {
	for field := range values {
		if field != "Bytes Downloaded" && field != "Bytes Uploaded" && field != "OpenFileHandles" {
			normalized.IgnoredValues++
		}
	}

	addCounter(values, "Bytes Downloaded", Series{
		Metric:    MetricStorageIO,
		Direction: DirectionRead,
	}, normalized)
	addCounter(values, "Bytes Uploaded", Series{
		Metric:    MetricStorageIO,
		Direction: DirectionWrite,
	}, normalized)
	addIntegerGauge(values, "OpenFileHandles", Series{
		Metric:    MetricOpenFiles,
		Component: ComponentAzStorage,
	}, normalized)
}

func normalizeLibfuse(values map[string]json.RawMessage, normalized *NormalizedRecord) {
	for field := range values {
		if field != "OpenFileHandles" && !isOperationField(field) {
			normalized.IgnoredValues++
		}
	}

	addIntegerGauge(values, "OpenFileHandles", Series{
		Metric:    MetricOpenFiles,
		Component: ComponentLibfuse,
	}, normalized)
	for _, field := range operationFields {
		addCounter(values, field.sourceName, Series{
			Metric:    MetricFSOperations,
			Operation: field.operation,
		}, normalized)
	}
}

func normalizeFileCache(values map[string]json.RawMessage, normalized *NormalizedRecord) {
	for field := range values {
		switch field {
		case "Files Downloaded", "Files served from cache", "Cache Usage", "Usage Percent":
		default:
			normalized.IgnoredValues++
		}
	}

	addCounter(values, "Files Downloaded", Series{Metric: MetricCacheDownloads}, normalized)
	addCounter(values, "Files served from cache", Series{Metric: MetricCacheHits}, normalized)
	addFormattedGauge(values, "Cache Usage", Series{Metric: MetricCacheUsage}, parseCacheUsage, normalized)
	addFormattedGauge(values, "Usage Percent", Series{Metric: MetricCacheUtilization}, parsePercentage, normalized)
}

func normalizeMemory(raw json.RawMessage, normalized *NormalizedRecord) {
	if raw == nil {
		return
	}

	var value string
	if !decodeSourceValue(raw, &value) {
		normalized.InvalidValues++
		return
	}
	bytesValue, ok := parseMemorySize(value)
	if !ok {
		normalized.InvalidValues++
		return
	}
	normalized.Gauges = append(normalized.Gauges, GaugeValue{
		Series: Series{Metric: MetricMemoryVirtual},
		Value:  bytesValue,
	})
}

func normalizeUnsupportedArray(raw json.RawMessage, normalized *NormalizedRecord) {
	if raw == nil {
		return
	}

	var values []json.RawMessage
	if !decodeSourceValue(raw, &values) {
		normalized.InvalidValues++
		return
	}
	normalized.IgnoredValues += len(values)
}

func normalizeUnsupportedString(raw json.RawMessage, normalized *NormalizedRecord) {
	if raw == nil {
		return
	}

	var value string
	if !decodeSourceValue(raw, &value) {
		normalized.InvalidValues++
		return
	}
	normalized.IgnoredValues++
}

func validateEventFields(fields map[string]json.RawMessage, normalized *NormalizedRecord) {
	for _, field := range []string{"timestamp", "componentName", "operation", "path"} {
		raw := fields[field]
		if raw == nil {
			continue
		}
		var value string
		if !decodeSourceValue(raw, &value) {
			normalized.InvalidValues++
		}
	}

	if raw := fields["value"]; raw != nil {
		var value map[string]json.RawMessage
		if !decodeSourceValue(raw, &value) || value == nil {
			normalized.InvalidValues++
		}
	}
}

func validateMessageTimestamp(raw json.RawMessage, normalized *NormalizedRecord) {
	if raw == nil {
		return
	}

	var value string
	if !decodeSourceValue(raw, &value) {
		normalized.InvalidValues++
		return
	}
	if _, err := time.Parse(time.RFC3339, value); err != nil {
		normalized.InvalidValues++
	}
}

func addCounter(values map[string]json.RawMessage, field string, series Series, normalized *NormalizedRecord) {
	raw, found := values[field]
	if !found {
		return
	}
	value, ok := parseSourceInteger(raw)
	if !ok {
		normalized.InvalidValues++
		return
	}
	normalized.Counters = append(normalized.Counters, CounterValue{Series: series, Value: value})
}

func addIntegerGauge(values map[string]json.RawMessage, field string, series Series, normalized *NormalizedRecord) {
	raw, found := values[field]
	if !found {
		return
	}
	value, ok := parseSourceInteger(raw)
	if !ok {
		normalized.InvalidValues++
		return
	}
	normalized.Gauges = append(normalized.Gauges, GaugeValue{Series: series, Value: float64(value)})
}

func addFormattedGauge(
	values map[string]json.RawMessage,
	field string,
	series Series,
	parse func(string) (float64, bool),
	normalized *NormalizedRecord,
) {
	raw, found := values[field]
	if !found {
		return
	}
	var sourceValue string
	if !decodeSourceValue(raw, &sourceValue) {
		normalized.InvalidValues++
		return
	}
	value, ok := parse(sourceValue)
	if !ok {
		normalized.InvalidValues++
		return
	}
	normalized.Gauges = append(normalized.Gauges, GaugeValue{Series: series, Value: value})
}

func parseSourceInteger(raw json.RawMessage) (uint64, bool) {
	var value any
	if !decodeSourceValue(raw, &value) {
		return 0, false
	}
	number, ok := value.(json.Number)
	if !ok {
		return 0, false
	}

	rational, ok := new(big.Rat).SetString(number.String())
	if !ok || !rational.IsInt() || rational.Sign() < 0 || rational.Num().Cmp(maxExactSourceInteger) > 0 {
		return 0, false
	}
	return rational.Num().Uint64(), true
}

func parseCacheUsage(value string) (float64, bool) {
	fields := strings.Fields(value)
	if len(fields) != 2 || fields[1] != "MB" {
		return 0, false
	}
	return parseScaledNonNegative(fields[0], 1024*1024)
}

func parsePercentage(value string) (float64, bool) {
	value = strings.TrimSpace(value)
	if !strings.HasSuffix(value, "%") {
		return 0, false
	}
	return parseScaledNonNegative(strings.TrimSpace(strings.TrimSuffix(value, "%")), 0.01)
}

func parseMemorySize(value string) (float64, bool) {
	value = strings.TrimSpace(value)
	if len(value) < 2 {
		return 0, false
	}

	var multiplier float64
	switch value[len(value)-1] {
	case 'k', 'K':
		multiplier = 1 << 10
	case 'm', 'M':
		multiplier = 1 << 20
	case 'g', 'G':
		multiplier = 1 << 30
	case 't', 'T':
		multiplier = 1 << 40
	case 'p', 'P':
		multiplier = 1 << 50
	case 'e', 'E':
		multiplier = 1 << 60
	default:
		return 0, false
	}

	return parseScaledNonNegative(strings.TrimSpace(value[:len(value)-1]), multiplier)
}

func parseScaledNonNegative(value string, multiplier float64) (float64, bool) {
	number, err := strconv.ParseFloat(value, 64)
	if err != nil || math.IsNaN(number) || math.IsInf(number, 0) || number < 0 {
		return 0, false
	}
	scaled := number * multiplier
	if math.IsNaN(scaled) || math.IsInf(scaled, 0) {
		return 0, false
	}
	return scaled, true
}

func decodeSourceValue(raw json.RawMessage, target any) bool {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || bytes.Equal(raw, []byte("null")) {
		return false
	}

	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if err := decoder.Decode(target); err != nil {
		return false
	}
	return errors.Is(decoder.Decode(&struct{}{}), io.EOF)
}

func isKnownTopLevelField(field string) bool {
	switch field {
	case "Timestamp", "BlobfuseStats", "FileCache", "CPUUsage", "MemoryUsage", "NetworkUsage":
		return true
	default:
		return false
	}
}

func isKnownMessageField(field string) bool {
	switch field {
	case "timestamp", "componentName", "operation", "path", "value":
		return true
	default:
		return false
	}
}

func isOperationField(field string) bool {
	for _, operation := range operationFields {
		if operation.sourceName == field {
			return true
		}
	}
	return false
}
