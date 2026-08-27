//go:build linux

package source

import (
	"errors"
	"math"
	"os"
	"strconv"
	"strings"
	"testing"
)

func TestProcessMetricsReaderConvertsProcfsUnits(t *testing.T) {
	identity := ProcessIdentity{PID: 1234, StartTicks: 250}
	reader := processMetricsReader{
		procRoot:   "/test-proc",
		clockTicks: 100,
		pageSize:   4096,
		readFile: func(string) ([]byte, error) {
			return []byte(processStatMetrics(1234, "blob fuse ) worker", 125, 75, 250, 42)), nil
		},
	}

	got, err := reader.read(identity)
	if err != nil {
		t.Fatalf("read() error = %v", err)
	}
	if got.CPUUserSeconds != 1.25 || got.CPUSystemSeconds != 0.75 || got.MemoryUsageBytes != 42*4096 {
		t.Fatalf("metrics = %#v", got)
	}
}

func TestProcessMetricsReaderRejectsChangedIdentity(t *testing.T) {
	reader := processMetricsReader{
		procRoot:   "/test-proc",
		clockTicks: 100,
		pageSize:   4096,
		readFile: func(string) ([]byte, error) {
			return []byte(processStatMetrics(1234, "blobfuse2", 1, 2, 251, 3)), nil
		},
	}

	_, err := reader.read(ProcessIdentity{PID: 1234, StartTicks: 250})
	if !errors.Is(err, ErrProcessIdentityChanged) {
		t.Fatalf("read() error = %v, want ErrProcessIdentityChanged", err)
	}
}

func TestProcessMetricsReaderDegradesOnUnavailableProcess(t *testing.T) {
	reader := processMetricsReader{
		procRoot:   "/test-proc",
		clockTicks: 100,
		pageSize:   4096,
		readFile: func(string) ([]byte, error) {
			return nil, os.ErrNotExist
		},
	}

	if _, err := reader.read(ProcessIdentity{PID: 1234, StartTicks: 250}); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("read() error = %v, want os.ErrNotExist", err)
	}
}

func TestProcessMetricsReaderRejectsResidentMemoryOverflow(t *testing.T) {
	reader := processMetricsReader{
		procRoot:   "/test-proc",
		clockTicks: 100,
		pageSize:   4096,
		readFile: func(string) ([]byte, error) {
			return []byte(processStatMetrics(1234, "blobfuse2", 1, 2, 250, math.MaxUint64)), nil
		},
	}

	if _, err := reader.read(ProcessIdentity{PID: 1234, StartTicks: 250}); err == nil {
		t.Fatal("read() error = nil")
	}
}

func TestParseProcessStatRejectsInvalidMetricFields(t *testing.T) {
	tests := []string{
		processStatMetrics(1234, "blobfuse2", 1, 2, 250, 3)[:20],
		"1234 (blobfuse2) S 0 0 0 0 0 0 0 0 0 0 invalid 2 0 0 0 0 0 0 250 0 3",
		"1234 (blobfuse2) S 0 0 0 0 0 0 0 0 0 0 1 invalid 0 0 0 0 0 0 0 250 0 3",
		"1234 (blobfuse2) S 0 0 0 0 0 0 0 0 0 0 1 2 0 0 0 0 0 0 250 0 -1",
	}
	for _, stat := range tests {
		if _, err := parseProcessStat([]byte(stat), 1234); err == nil {
			t.Fatalf("parseProcessStat(%q) error = nil", stat)
		}
	}
}

func processStatMetrics(
	pid int,
	command string,
	userTicks uint64,
	systemTicks uint64,
	startTicks uint64,
	residentPages uint64,
) string {
	fields := []string{"S"}
	for index := 1; index <= 21; index++ {
		fields = append(fields, "0")
	}
	fields[11] = strconv.FormatUint(userTicks, 10)
	fields[12] = strconv.FormatUint(systemTicks, 10)
	fields[19] = strconv.FormatUint(startTicks, 10)
	fields[21] = strconv.FormatUint(residentPages, 10)
	return strconv.Itoa(pid) + " (" + command + ") " + strings.Join(fields, " ") + "\n"
}
