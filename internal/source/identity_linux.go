//go:build linux

package source

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/tklauser/go-sysconf"
)

var ErrProcessIdentityChanged = errors.New("process identity changed while reading procfs")

// ProcessIdentity binds a live PID to one process lifetime on one host boot.
type ProcessIdentity struct {
	BootID       string
	PID          int
	StartTicks   uint64
	CreationTime time.Time
}

// ReadProcessIdentity reads and verifies the identity of one live Linux process.
func ReadProcessIdentity(pid int) (ProcessIdentity, error) {
	clockTicks, err := sysconf.Sysconf(sysconf.SC_CLK_TCK)
	if err != nil {
		return ProcessIdentity{}, fmt.Errorf("read clock ticks per second: %w", err)
	}
	if clockTicks <= 0 {
		return ProcessIdentity{}, fmt.Errorf("read clock ticks per second: got %d", clockTicks)
	}

	return processIdentityReader{
		procRoot:   "/proc",
		clockTicks: uint64(clockTicks),
		readFile:   os.ReadFile,
	}.read(pid)
}

// InstanceID returns the privacy-bounded OpenTelemetry service instance ID.
func (identity ProcessIdentity) InstanceID() string {
	payload := strings.Join([]string{
		"blobfuse-health-exporter/v0",
		identity.BootID,
		strconv.Itoa(identity.PID),
		strconv.FormatUint(identity.StartTicks, 10),
	}, "\x00")
	digest := sha256.Sum256([]byte(payload))
	return hex.EncodeToString(digest[:])
}

// SameSession reports whether two observations identify the same process lifetime.
func (identity ProcessIdentity) SameSession(other ProcessIdentity) bool {
	return identity.BootID == other.BootID &&
		identity.PID == other.PID &&
		identity.StartTicks == other.StartTicks
}

type processIdentityReader struct {
	procRoot   string
	clockTicks uint64
	readFile   func(string) ([]byte, error)
}

func (reader processIdentityReader) read(pid int) (ProcessIdentity, error) {
	if pid <= 0 {
		return ProcessIdentity{}, fmt.Errorf("pid must be positive")
	}
	if reader.procRoot == "" || reader.readFile == nil {
		return ProcessIdentity{}, fmt.Errorf("procfs reader is not configured")
	}
	if reader.clockTicks == 0 || reader.clockTicks > 1_000_000_000 {
		return ProcessIdentity{}, fmt.Errorf("clock ticks per second is invalid: %d", reader.clockTicks)
	}

	startTicks, err := reader.readStartTicks(pid)
	if err != nil {
		return ProcessIdentity{}, err
	}
	bootID, err := reader.readBootID()
	if err != nil {
		return ProcessIdentity{}, err
	}
	bootTime, err := reader.readBootTime()
	if err != nil {
		return ProcessIdentity{}, err
	}
	verifiedStartTicks, err := reader.readStartTicks(pid)
	if err != nil {
		return ProcessIdentity{}, err
	}
	if startTicks != verifiedStartTicks {
		return ProcessIdentity{}, fmt.Errorf(
			"%w: pid %d changed from start ticks %d to %d",
			ErrProcessIdentityChanged,
			pid,
			startTicks,
			verifiedStartTicks,
		)
	}

	creationTime, err := processCreationTime(bootTime, startTicks, reader.clockTicks)
	if err != nil {
		return ProcessIdentity{}, err
	}

	return ProcessIdentity{
		BootID:       bootID,
		PID:          pid,
		StartTicks:   startTicks,
		CreationTime: creationTime,
	}, nil
}

func (reader processIdentityReader) readStartTicks(pid int) (uint64, error) {
	path := filepath.Join(reader.procRoot, strconv.Itoa(pid), "stat")
	data, err := reader.readFile(path)
	if err != nil {
		return 0, fmt.Errorf("read process stat for pid %d: %w", pid, err)
	}

	startTicks, err := parseProcessStartTicks(data, pid)
	if err != nil {
		return 0, fmt.Errorf("parse process stat for pid %d: %w", pid, err)
	}
	return startTicks, nil
}

func (reader processIdentityReader) readBootID() (string, error) {
	data, err := reader.readFile(filepath.Join(reader.procRoot, "sys", "kernel", "random", "boot_id"))
	if err != nil {
		return "", fmt.Errorf("read host boot ID: %w", err)
	}

	bootID := strings.ToLower(strings.TrimSpace(string(data)))
	if !isCanonicalBootID(bootID) {
		return "", fmt.Errorf("read host boot ID: invalid format")
	}
	return bootID, nil
}

func (reader processIdentityReader) readBootTime() (int64, error) {
	data, err := reader.readFile(filepath.Join(reader.procRoot, "stat"))
	if err != nil {
		return 0, fmt.Errorf("read host boot time: %w", err)
	}

	scanner := bufio.NewScanner(bytes.NewReader(data))
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) == 0 || fields[0] != "btime" {
			continue
		}
		if len(fields) != 2 {
			return 0, fmt.Errorf("read host boot time: invalid btime field")
		}
		bootTime, err := strconv.ParseInt(fields[1], 10, 64)
		if err != nil || bootTime <= 0 {
			return 0, fmt.Errorf("read host boot time: invalid btime value")
		}
		return bootTime, nil
	}
	if err := scanner.Err(); err != nil {
		return 0, fmt.Errorf("read host boot time: %w", err)
	}
	return 0, fmt.Errorf("read host boot time: btime field is missing")
}

func parseProcessStartTicks(data []byte, expectedPID int) (uint64, error) {
	stat := strings.TrimSpace(string(data))
	openParen := strings.IndexByte(stat, '(')
	closeParen := strings.LastIndexByte(stat, ')')
	if openParen < 1 || closeParen <= openParen {
		return 0, fmt.Errorf("invalid process stat framing")
	}

	pid, err := strconv.Atoi(strings.TrimSpace(stat[:openParen]))
	if err != nil || pid != expectedPID {
		return 0, fmt.Errorf("process stat pid does not match %d", expectedPID)
	}

	fields := strings.Fields(stat[closeParen+1:])
	const startTimeIndexAfterCommand = 19
	if len(fields) <= startTimeIndexAfterCommand {
		return 0, fmt.Errorf("process stat is missing start time")
	}
	if len(fields[0]) != 1 {
		return 0, fmt.Errorf("process stat has invalid state")
	}

	startTicks, err := strconv.ParseUint(fields[startTimeIndexAfterCommand], 10, 64)
	if err != nil {
		return 0, fmt.Errorf("process stat has invalid start time")
	}
	return startTicks, nil
}

func processCreationTime(bootTime int64, startTicks, clockTicks uint64) (time.Time, error) {
	wholeSeconds := startTicks / clockTicks
	if wholeSeconds > math.MaxInt64 || bootTime > math.MaxInt64-int64(wholeSeconds) {
		return time.Time{}, fmt.Errorf("process creation time overflows")
	}

	remainder := startTicks % clockTicks
	nanoseconds := remainder * uint64(time.Second) / clockTicks
	return time.Unix(bootTime+int64(wholeSeconds), int64(nanoseconds)).UTC(), nil
}

func isCanonicalBootID(value string) bool {
	if len(value) != 36 {
		return false
	}
	for index, character := range value {
		switch index {
		case 8, 13, 18, 23:
			if character != '-' {
				return false
			}
		default:
			if !((character >= '0' && character <= '9') || (character >= 'a' && character <= 'f')) {
				return false
			}
		}
	}
	return true
}
