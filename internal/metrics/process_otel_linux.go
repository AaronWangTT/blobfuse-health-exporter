//go:build linux

package metrics

import (
	"context"
	"fmt"
	"sync"

	"github.com/AaronWangTT/blobfuse-health-exporter/internal/source"
	"go.opentelemetry.io/otel/attribute"
	otelmetric "go.opentelemetry.io/otel/metric"
)

const (
	ProcessCPUTimeName        = "process.cpu.time"
	ProcessCPUTimeDescription = "Total CPU seconds broken down by different states."
	ProcessCPUTimeUnit        = "s"
	ProcessMemoryUsageName    = "process.memory.usage"
	ProcessMemoryDescription  = "The amount of physical memory in use."
	ProcessMemoryUsageUnit    = "By"
	AttributeProcessCPUState  = "process.cpu.state"
	ProcessCPUStateUser       = "user"
	ProcessCPUStateSystem     = "system"
)

type processMetricsReader func(source.ProcessIdentity) (source.ProcessMetrics, error)

type ProcessRecorder struct {
	identity     source.ProcessIdentity
	read         processMetricsReader
	cpuTime      otelmetric.Float64ObservableCounter
	memoryUsage  otelmetric.Int64ObservableUpDownCounter
	registration otelmetric.Registration
	mutex        sync.RWMutex
	readErrors   uint64
}

func NewProcessRecorder(meter otelmetric.Meter, identity source.ProcessIdentity) (*ProcessRecorder, error) {
	return newProcessRecorder(meter, identity, source.ReadProcessMetrics)
}

func newProcessRecorder(
	meter otelmetric.Meter,
	identity source.ProcessIdentity,
	read processMetricsReader,
) (*ProcessRecorder, error) {
	if meter == nil {
		return nil, fmt.Errorf("OpenTelemetry meter is required")
	}
	if identity.PID <= 0 || identity.StartTicks == 0 {
		return nil, fmt.Errorf("process identity is invalid")
	}
	if read == nil {
		return nil, fmt.Errorf("process metric reader is required")
	}

	cpuTime, err := meter.Float64ObservableCounter(
		ProcessCPUTimeName,
		otelmetric.WithDescription(ProcessCPUTimeDescription),
		otelmetric.WithUnit(ProcessCPUTimeUnit),
	)
	if err != nil {
		return nil, err
	}
	memoryUsage, err := meter.Int64ObservableUpDownCounter(
		ProcessMemoryUsageName,
		otelmetric.WithDescription(ProcessMemoryDescription),
		otelmetric.WithUnit(ProcessMemoryUsageUnit),
	)
	if err != nil {
		return nil, err
	}

	recorder := &ProcessRecorder{
		identity:    identity,
		read:        read,
		cpuTime:     cpuTime,
		memoryUsage: memoryUsage,
	}
	registration, err := meter.RegisterCallback(
		recorder.observe,
		cpuTime,
		memoryUsage,
	)
	if err != nil {
		return nil, err
	}
	recorder.registration = registration
	return recorder, nil
}

func (recorder *ProcessRecorder) Close() error {
	if recorder == nil || recorder.registration == nil {
		return nil
	}
	return recorder.registration.Unregister()
}

func (recorder *ProcessRecorder) ReadErrors() uint64 {
	if recorder == nil {
		return 0
	}
	recorder.mutex.RLock()
	defer recorder.mutex.RUnlock()
	return recorder.readErrors
}

func (recorder *ProcessRecorder) observe(_ context.Context, observer otelmetric.Observer) error {
	processMetrics, err := recorder.read(recorder.identity)
	if err != nil {
		recorder.mutex.Lock()
		recorder.readErrors++
		recorder.mutex.Unlock()
		return nil
	}

	observer.ObserveFloat64(
		recorder.cpuTime,
		processMetrics.CPUUserSeconds,
		otelmetric.WithAttributes(attribute.String(AttributeProcessCPUState, ProcessCPUStateUser)),
	)
	observer.ObserveFloat64(
		recorder.cpuTime,
		processMetrics.CPUSystemSeconds,
		otelmetric.WithAttributes(attribute.String(AttributeProcessCPUState, ProcessCPUStateSystem)),
	)
	observer.ObserveInt64(recorder.memoryUsage, processMetrics.MemoryUsageBytes)
	return nil
}
