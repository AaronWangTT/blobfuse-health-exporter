//go:build linux

package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/tklauser/go-sysconf"
)

type options struct {
	pid              int
	duration         time.Duration
	interval         time.Duration
	maxMedianCPU     float64
	maxAverageCPU    float64
	maxResidentBytes uint64
}

type sample struct {
	time          time.Time
	processTicks  uint64
	residentBytes uint64
}

type report struct {
	Samples           int     `json:"samples"`
	DurationSeconds   float64 `json:"duration_seconds"`
	MedianCPUPercent  float64 `json:"median_cpu_percent"`
	AverageCPUPercent float64 `json:"average_cpu_percent"`
	P95CPUPercent     float64 `json:"p95_cpu_percent"`
	MaxResidentBytes  uint64  `json:"max_resident_bytes"`
}

func main() {
	settings, err := parseOptions(os.Args[1:])
	if err != nil {
		fmt.Fprintf(os.Stderr, "procstat configuration error: %v\n", err)
		os.Exit(2)
	}
	result, err := measure(settings)
	if err != nil {
		fmt.Fprintf(os.Stderr, "procstat measurement error: %v\n", err)
		os.Exit(1)
	}
	encoder := json.NewEncoder(os.Stdout)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(result); err != nil {
		fmt.Fprintf(os.Stderr, "encode procstat report: %v\n", err)
		os.Exit(1)
	}
	if settings.maxMedianCPU > 0 && result.MedianCPUPercent >= settings.maxMedianCPU {
		fmt.Fprintf(os.Stderr, "median CPU %.3f%% is not below %.3f%%\n", result.MedianCPUPercent, settings.maxMedianCPU)
		os.Exit(1)
	}
	if settings.maxAverageCPU > 0 && result.AverageCPUPercent >= settings.maxAverageCPU {
		fmt.Fprintf(os.Stderr, "average CPU %.3f%% is not below %.3f%%\n", result.AverageCPUPercent, settings.maxAverageCPU)
		os.Exit(1)
	}
	if result.MaxResidentBytes >= settings.maxResidentBytes {
		fmt.Fprintf(os.Stderr, "resident memory %d is not below %d bytes\n", result.MaxResidentBytes, settings.maxResidentBytes)
		os.Exit(1)
	}
}

func parseOptions(args []string) (options, error) {
	var settings options
	flags := flag.NewFlagSet("procstat", flag.ContinueOnError)
	flags.SetOutput(os.Stderr)
	flags.IntVar(&settings.pid, "pid", 0, "process ID to sample")
	flags.DurationVar(&settings.duration, "duration", 5*time.Minute, "measurement duration")
	flags.DurationVar(&settings.interval, "interval", time.Second, "sample interval")
	flags.Float64Var(&settings.maxMedianCPU, "max-median-cpu", 1, "exclusive median CPU limit in percent of one core")
	flags.Float64Var(&settings.maxAverageCPU, "max-average-cpu", 0, "exclusive average CPU limit in percent of one core; zero disables it")
	flags.Uint64Var(&settings.maxResidentBytes, "max-rss-bytes", 64*1024*1024, "exclusive resident-memory limit")
	if err := flags.Parse(args); err != nil {
		return options{}, err
	}
	if flags.NArg() != 0 {
		return options{}, fmt.Errorf("positional arguments are not supported")
	}
	if settings.pid <= 0 {
		return options{}, fmt.Errorf("PID must be positive")
	}
	if settings.duration <= 0 || settings.interval <= 0 || settings.duration < settings.interval {
		return options{}, fmt.Errorf("duration and interval must be positive, with duration at least one interval")
	}
	if settings.maxMedianCPU < 0 || settings.maxAverageCPU < 0 || settings.maxResidentBytes == 0 {
		return options{}, fmt.Errorf("CPU limits cannot be negative and the memory limit must be positive")
	}
	return settings, nil
}

func measure(settings options) (report, error) {
	clockTicks, err := sysconf.Sysconf(sysconf.SC_CLK_TCK)
	if err != nil || clockTicks <= 0 {
		return report{}, fmt.Errorf("read clock ticks per second: %w", err)
	}
	previous, err := readSample(settings.pid)
	if err != nil {
		return report{}, err
	}
	started := previous.time
	maxResidentBytes := previous.residentBytes
	var cpuPercentages []float64
	ticker := time.NewTicker(settings.interval)
	defer ticker.Stop()
	timer := time.NewTimer(settings.duration)
	defer timer.Stop()

	for {
		select {
		case <-ticker.C:
			current, err := readSample(settings.pid)
			if err != nil {
				return report{}, err
			}
			elapsed := current.time.Sub(previous.time).Seconds()
			if current.processTicks < previous.processTicks || elapsed <= 0 {
				return report{}, fmt.Errorf("process counters moved backwards")
			}
			cpu := float64(current.processTicks-previous.processTicks) / float64(clockTicks) / elapsed * 100
			cpuPercentages = append(cpuPercentages, cpu)
			if current.residentBytes > maxResidentBytes {
				maxResidentBytes = current.residentBytes
			}
			previous = current

		case <-timer.C:
			if len(cpuPercentages) == 0 {
				return report{}, fmt.Errorf("no samples collected")
			}
			return report{
				Samples:           len(cpuPercentages),
				DurationSeconds:   time.Since(started).Seconds(),
				MedianCPUPercent:  percentile(cpuPercentages, 0.50),
				AverageCPUPercent: average(cpuPercentages),
				P95CPUPercent:     percentile(cpuPercentages, 0.95),
				MaxResidentBytes:  maxResidentBytes,
			}, nil
		}
	}
}

func readSample(pid int) (sample, error) {
	stat, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return sample{}, fmt.Errorf("read process stat: %w", err)
	}
	processTicks, err := parseProcessTicks(stat)
	if err != nil {
		return sample{}, err
	}
	status, err := os.ReadFile(fmt.Sprintf("/proc/%d/status", pid))
	if err != nil {
		return sample{}, fmt.Errorf("read process status: %w", err)
	}
	residentBytes, err := parseResidentBytes(status)
	if err != nil {
		return sample{}, err
	}
	return sample{time: time.Now(), processTicks: processTicks, residentBytes: residentBytes}, nil
}

func parseProcessTicks(data []byte) (uint64, error) {
	closingParenthesis := strings.LastIndexByte(string(data), ')')
	if closingParenthesis < 0 {
		return 0, fmt.Errorf("process stat command is not terminated")
	}
	fields := strings.Fields(string(data[closingParenthesis+1:]))
	if len(fields) <= 12 {
		return 0, fmt.Errorf("process stat has %d fields after command", len(fields))
	}
	userTicks, err := strconv.ParseUint(fields[11], 10, 64)
	if err != nil {
		return 0, fmt.Errorf("parse process user ticks: %w", err)
	}
	systemTicks, err := strconv.ParseUint(fields[12], 10, 64)
	if err != nil {
		return 0, fmt.Errorf("parse process system ticks: %w", err)
	}
	if ^uint64(0)-userTicks < systemTicks {
		return 0, fmt.Errorf("process CPU ticks overflow")
	}
	return userTicks + systemTicks, nil
}

func parseResidentBytes(data []byte) (uint64, error) {
	for _, line := range strings.Split(string(data), "\n") {
		fields := strings.Fields(line)
		if len(fields) == 3 && fields[0] == "VmRSS:" && fields[2] == "kB" {
			kilobytes, err := strconv.ParseUint(fields[1], 10, 64)
			if err != nil {
				return 0, fmt.Errorf("parse resident memory: %w", err)
			}
			if kilobytes > ^uint64(0)/1024 {
				return 0, fmt.Errorf("resident memory overflows bytes")
			}
			return kilobytes * 1024, nil
		}
	}
	return 0, errors.New("process status does not contain VmRSS")
}

func percentile(values []float64, fraction float64) float64 {
	ordered := append([]float64(nil), values...)
	sort.Float64s(ordered)
	index := int(float64(len(ordered)-1) * fraction)
	return ordered[index]
}

func average(values []float64) float64 {
	var total float64
	for _, value := range values {
		total += value
	}
	return total / float64(len(values))
}
