//go:build linux

package source

import (
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strconv"

	"github.com/tklauser/go-sysconf"
)

type ProcessMetrics struct {
	CPUUserSeconds   float64
	CPUSystemSeconds float64
	MemoryUsageBytes int64
}

func ReadProcessMetrics(identity ProcessIdentity) (ProcessMetrics, error) {
	clockTicks, err := sysconf.Sysconf(sysconf.SC_CLK_TCK)
	if err != nil {
		return ProcessMetrics{}, fmt.Errorf("read clock ticks per second: %w", err)
	}
	pageSize, err := sysconf.Sysconf(sysconf.SC_PAGESIZE)
	if err != nil {
		return ProcessMetrics{}, fmt.Errorf("read page size: %w", err)
	}
	if clockTicks <= 0 || pageSize <= 0 {
		return ProcessMetrics{}, fmt.Errorf(
			"read procfs units: clock ticks=%d page size=%d",
			clockTicks,
			pageSize,
		)
	}

	return processMetricsReader{
		procRoot:   "/proc",
		clockTicks: uint64(clockTicks),
		pageSize:   uint64(pageSize),
		readFile:   os.ReadFile,
	}.read(identity)
}

type processMetricsReader struct {
	procRoot   string
	clockTicks uint64
	pageSize   uint64
	readFile   func(string) ([]byte, error)
}

func (reader processMetricsReader) read(identity ProcessIdentity) (ProcessMetrics, error) {
	if identity.PID <= 0 || identity.StartTicks == 0 {
		return ProcessMetrics{}, fmt.Errorf("process identity is invalid")
	}
	if reader.procRoot == "" || reader.readFile == nil {
		return ProcessMetrics{}, fmt.Errorf("procfs metric reader is not configured")
	}
	if reader.clockTicks == 0 || reader.pageSize == 0 {
		return ProcessMetrics{}, fmt.Errorf("procfs metric units are invalid")
	}

	path := filepath.Join(reader.procRoot, strconv.Itoa(identity.PID), "stat")
	data, err := reader.readFile(path)
	if err != nil {
		return ProcessMetrics{}, fmt.Errorf("read process metrics for pid %d: %w", identity.PID, err)
	}
	stat, err := parseProcessStat(data, identity.PID)
	if err != nil {
		return ProcessMetrics{}, fmt.Errorf("parse process metrics for pid %d: %w", identity.PID, err)
	}
	if stat.StartTicks != identity.StartTicks {
		return ProcessMetrics{}, fmt.Errorf(
			"%w: pid %d changed from start ticks %d to %d",
			ErrProcessIdentityChanged,
			identity.PID,
			identity.StartTicks,
			stat.StartTicks,
		)
	}
	if stat.ResidentPages > math.MaxInt64/reader.pageSize {
		return ProcessMetrics{}, fmt.Errorf("resident memory byte count overflows")
	}

	return ProcessMetrics{
		CPUUserSeconds:   float64(stat.UserTicks) / float64(reader.clockTicks),
		CPUSystemSeconds: float64(stat.SystemTicks) / float64(reader.clockTicks),
		MemoryUsageBytes: int64(stat.ResidentPages * reader.pageSize),
	}, nil
}
