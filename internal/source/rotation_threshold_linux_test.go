//go:build linux

package source

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

const blobFuseReportRotationBytes = 10 * 1024 * 1024

func TestGenerationRegistryRetainsExactTenMiBRolloverChain(t *testing.T) {
	path := secureTempDir(t)
	identity := testIdentity()
	base := filepath.Join(path, "monitor_1234")
	currentPath := base + ".json"
	prefix := `[{"n":0}`
	writeReport(
		t,
		currentPath,
		prefix+strings.Repeat(" ", blobFuseReportRotationBytes-len(prefix)-1)+`]`,
		identity.CreationTime.Add(10*time.Second),
		0o600,
	)
	for rotation := 1; rotation <= 9; rotation++ {
		writeReport(
			t,
			fmt.Sprintf("%s_%d.json", base, rotation),
			fmt.Sprintf(`[{"n":%d}]`, rotation),
			identity.CreationTime.Add(time.Duration(10-rotation)*time.Second),
			0o600,
		)
	}
	stat, err := os.Stat(currentPath)
	if err != nil {
		t.Fatalf("Stat() error = %v", err)
	}
	if stat.Size() != blobFuseReportRotationBytes {
		t.Fatalf("current size = %d, want %d", stat.Size(), blobFuseReportRotationBytes)
	}

	directory := openTestDirectory(t, path)
	registry, err := newGenerationRegistry(16 * 1024 * 1024)
	if err != nil {
		t.Fatalf("newGenerationRegistry() error = %v", err)
	}
	t.Cleanup(func() { registry.close() })
	initial, err := directory.ScanReports(identity)
	if err != nil {
		t.Fatalf("initial ScanReports() error = %v", err)
	}
	initialIDs := make(map[int]GenerationID, len(initial))
	for _, generation := range initial {
		initialIDs[generation.Rotation] = generation.Report.Generation
	}
	if registered, err := registry.register(initial); err != nil || registered != 10 {
		t.Fatalf("initial register() = %d, %v; want 10, nil", registered, err)
	}
	evictedReader := registry.readers[initialIDs[9]]

	if err := os.Remove(base + "_9.json"); err != nil {
		t.Fatalf("Remove(_9) error = %v", err)
	}
	for rotation := 8; rotation >= 1; rotation-- {
		if err := os.Rename(
			fmt.Sprintf("%s_%d.json", base, rotation),
			fmt.Sprintf("%s_%d.json", base, rotation+1),
		); err != nil {
			t.Fatalf("Rename(%d -> %d) error = %v", rotation, rotation+1, err)
		}
	}
	if err := os.Rename(currentPath, base+"_1.json"); err != nil {
		t.Fatalf("Rename(current -> _1) error = %v", err)
	}
	writeReport(t, currentPath, "[", identity.CreationTime.Add(11*time.Second), 0o600)

	afterRotation, err := directory.ScanReports(identity)
	if err != nil {
		t.Fatalf("rotated ScanReports() error = %v", err)
	}
	var newCurrentID GenerationID
	for _, generation := range afterRotation {
		if generation.Rotation == 0 {
			newCurrentID = generation.Report.Generation
		}
	}
	if registered, err := registry.register(afterRotation); err != nil || registered != 1 {
		t.Fatalf("rotated register() = %d, %v; want 1, nil", registered, err)
	}

	buffer := make([]byte, 1)
	if _, err := evictedReader.report.ReadAt(buffer, 0); err != nil || buffer[0] != '[' {
		t.Fatalf("evicted descriptor read = %q, %v; want open descriptor", buffer, err)
	}
	readers := registry.oldestFirst()
	got := make([]GenerationID, 0, len(readers))
	for _, reader := range readers {
		got = append(got, reader.report.Generation)
	}
	want := make([]GenerationID, 0, 11)
	for rotation := 9; rotation >= 0; rotation-- {
		want = append(want, initialIDs[rotation])
	}
	want = append(want, newCurrentID)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ordered generation IDs = %#v, want %#v", got, want)
	}
}
