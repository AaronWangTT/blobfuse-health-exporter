//go:build linux

package source

import (
	"errors"
	"fmt"
	"os"

	"golang.org/x/sys/unix"
)

// DiscoveredGeneration is one validated report found during a directory scan.
// The caller owns Report and must close it.
type DiscoveredGeneration struct {
	Rotation int
	Report   *ReportFile
}

// ScanReports discovers report generations for exactly one process identity.
// A file that disappears between enumeration and opening is left for the next
// rescan; all other open or validation failures fail the scan closed.
func (directory *ReportDirectory) ScanReports(identity ProcessIdentity) ([]DiscoveredGeneration, error) {
	if directory == nil {
		return nil, os.ErrClosed
	}
	if identity.PID <= 0 || identity.CreationTime.IsZero() {
		return nil, fmt.Errorf("%w: process identity is incomplete", ErrUnsafeSource)
	}

	names, err := directory.readEntryNames()
	if err != nil {
		return nil, err
	}
	expectedNames, err := expectedReportNames(identity.PID)
	if err != nil {
		return nil, err
	}

	discovered := make([]DiscoveredGeneration, 0, len(expectedNames))
	byGeneration := make(map[GenerationID]int, len(expectedNames))
	for _, name := range names {
		rotation, expected := expectedNames[name]
		if !expected {
			continue
		}

		report, openErr := directory.OpenReport(identity, rotation)
		if errors.Is(openErr, os.ErrNotExist) {
			continue
		}
		if openErr != nil {
			closeDiscovered(discovered)
			return nil, fmt.Errorf("scan report generation %d: %w", rotation, openErr)
		}

		if existing, seen := byGeneration[report.Generation]; seen {
			if rotation > discovered[existing].Rotation {
				discovered[existing].Rotation = rotation
			}
			report.Close()
			continue
		}

		byGeneration[report.Generation] = len(discovered)
		discovered = append(discovered, DiscoveredGeneration{
			Rotation: rotation,
			Report:   report,
		})
	}

	return discovered, nil
}

func (directory *ReportDirectory) readEntryNames() ([]string, error) {
	directory.mutex.RLock()
	if directory.fileDescriptor < 0 {
		directory.mutex.RUnlock()
		return nil, os.ErrClosed
	}
	fileDescriptor, err := unix.Openat(
		directory.fileDescriptor,
		".",
		unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW,
		0,
	)
	directory.mutex.RUnlock()
	if err != nil {
		return nil, fmt.Errorf("duplicate report directory descriptor: %w", err)
	}

	file := os.NewFile(uintptr(fileDescriptor), "")
	if file == nil {
		unix.Close(fileDescriptor)
		return nil, fmt.Errorf("duplicate report directory descriptor: invalid descriptor")
	}
	defer file.Close()

	names, err := file.Readdirnames(-1)
	if err != nil {
		return nil, fmt.Errorf("enumerate report directory: %w", err)
	}
	return names, nil
}

func expectedReportNames(pid int) (map[string]int, error) {
	names := make(map[string]int, 10)
	for rotation := 0; rotation <= 9; rotation++ {
		name, err := reportName(pid, rotation)
		if err != nil {
			return nil, err
		}
		names[name] = rotation
	}
	return names, nil
}

func closeDiscovered(generations []DiscoveredGeneration) {
	for index := range generations {
		generations[index].Report.Close()
	}
}
