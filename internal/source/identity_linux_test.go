//go:build linux

package source

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestProcessIdentityReader(t *testing.T) {
	const (
		pid        = 1234
		startTicks = 250
		bootTime   = 1_700_000_000
	)
	procRoot := t.TempDir()
	writeProcFile(t, procRoot, "sys/kernel/random/boot_id", "4C1F5F44-7E22-4B5A-91D9-16D2D55F5C81\n")
	writeProcFile(t, procRoot, "stat", fmt.Sprintf("cpu  1 2 3 4\nbtime %d\n", bootTime))
	writeProcFile(t, procRoot, filepath.Join(strconv.Itoa(pid), "stat"), processStat(pid, "blob fuse ) worker", startTicks))

	identity, err := (processIdentityReader{
		procRoot:   procRoot,
		clockTicks: 100,
		readFile:   os.ReadFile,
	}).read(pid)
	if err != nil {
		t.Fatalf("read() error = %v", err)
	}

	wantCreationTime := time.Unix(bootTime, 0).Add(2500 * time.Millisecond).UTC()
	if identity.BootID != "4c1f5f44-7e22-4b5a-91d9-16d2d55f5c81" ||
		identity.PID != pid ||
		identity.StartTicks != startTicks ||
		!identity.CreationTime.Equal(wantCreationTime) {
		t.Fatalf("identity = %#v, want pid=%d start=%d creation=%v", identity, pid, startTicks, wantCreationTime)
	}

	payload := strings.Join([]string{
		"blobfuse-health-exporter/v0",
		identity.BootID,
		strconv.Itoa(pid),
		strconv.FormatUint(startTicks, 10),
	}, "\x00")
	wantDigest := sha256.Sum256([]byte(payload))
	if got, want := identity.InstanceID(), hex.EncodeToString(wantDigest[:]); got != want {
		t.Fatalf("InstanceID() = %q, want %q", got, want)
	}
}

func TestProcessIdentityReaderDetectsPIDReuse(t *testing.T) {
	const pid = 1234
	procRoot := "/test-proc"
	statPath := filepath.Join(procRoot, strconv.Itoa(pid), "stat")
	statReads := 0
	reader := processIdentityReader{
		procRoot:   procRoot,
		clockTicks: 100,
		readFile: func(path string) ([]byte, error) {
			switch path {
			case statPath:
				statReads++
				return []byte(processStat(pid, "blobfuse2", uint64(statReads*100))), nil
			case filepath.Join(procRoot, "sys", "kernel", "random", "boot_id"):
				return []byte("4c1f5f44-7e22-4b5a-91d9-16d2d55f5c81\n"), nil
			case filepath.Join(procRoot, "stat"):
				return []byte("btime 1700000000\n"), nil
			default:
				return nil, os.ErrNotExist
			}
		},
	}

	_, err := reader.read(pid)
	if !errors.Is(err, ErrProcessIdentityChanged) {
		t.Fatalf("read() error = %v, want ErrProcessIdentityChanged", err)
	}
}

func TestParseProcessStartTicksRejectsInvalidInput(t *testing.T) {
	tests := []struct {
		name string
		stat string
	}{
		{name: "missing framing", stat: "1234 blobfuse2 S 1 2 3"},
		{name: "wrong pid", stat: processStat(4321, "blobfuse2", 100)},
		{name: "missing fields", stat: "1234 (blobfuse2) S 1 2 3"},
		{name: "invalid ticks", stat: strings.Replace(processStat(1234, "blobfuse2", 100), " 100 ", " invalid ", 1)},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := parseProcessStartTicks([]byte(test.stat), 1234); err == nil {
				t.Fatal("parseProcessStartTicks() error = nil")
			}
		})
	}
}

func TestReadProcessIdentityForCurrentProcess(t *testing.T) {
	identity, err := ReadProcessIdentity(os.Getpid())
	if err != nil {
		t.Fatalf("ReadProcessIdentity() error = %v", err)
	}
	if identity.PID != os.Getpid() || identity.BootID == "" || identity.StartTicks == 0 {
		t.Fatalf("identity = %#v", identity)
	}
	if identity.CreationTime.After(time.Now().Add(time.Second)) {
		t.Fatalf("CreationTime = %v, want no later than now", identity.CreationTime)
	}
	if len(identity.InstanceID()) != sha256.Size*2 {
		t.Fatalf("InstanceID() length = %d, want %d", len(identity.InstanceID()), sha256.Size*2)
	}
	if !identity.SameSession(identity) {
		t.Fatal("SameSession() = false for identical identity")
	}
}

func processStat(pid int, command string, startTicks uint64) string {
	fields := []string{"S"}
	for field := 4; field <= 21; field++ {
		fields = append(fields, "0")
	}
	fields = append(fields, strconv.FormatUint(startTicks, 10), "0", "0")
	return fmt.Sprintf("%d (%s) %s\n", pid, command, strings.Join(fields, " "))
}

func writeProcFile(t *testing.T, root, relativePath, contents string) {
	t.Helper()
	path := filepath.Join(root, relativePath)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
}
