//go:build linux

package source

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/sys/unix"
)

var (
	ErrUnsafeSource    = errors.New("unsafe report source")
	ErrStaleGeneration = errors.New("stale report generation")
)

// GenerationID identifies one opened report across pathname rotation.
type GenerationID struct {
	Device uint64
	Inode  uint64
}

// ReportFile is one validated report generation held by descriptor.
type ReportFile struct {
	file             *os.File
	Generation       GenerationID
	Size             int64
	ModificationTime time.Time
}

func (report *ReportFile) ReadAt(buffer []byte, offset int64) (int, error) {
	if report == nil || report.file == nil {
		return 0, os.ErrClosed
	}
	return report.file.ReadAt(buffer, offset)
}

func (report *ReportFile) Close() error {
	if report == nil || report.file == nil {
		return nil
	}
	err := report.file.Close()
	report.file = nil
	return err
}

// ReportDirectory owns one validated directory descriptor for a source session.
type ReportDirectory struct {
	mutex          sync.RWMutex
	fileDescriptor int
	ownerUID       uint32
	allowInsecure  bool
}

// OpenReportDirectory validates and opens an absolute report directory without following symlinks.
func OpenReportDirectory(path string, allowInsecure bool) (*ReportDirectory, error) {
	if !filepath.IsAbs(path) {
		return nil, fmt.Errorf("%w: report directory must be absolute", ErrUnsafeSource)
	}

	fileDescriptor, err := openDirectoryNoFollow(filepath.Clean(path))
	if err != nil {
		return nil, err
	}

	ownerUID := uint32(os.Geteuid())
	var stat unix.Stat_t
	if err := unix.Fstat(fileDescriptor, &stat); err != nil {
		unix.Close(fileDescriptor)
		return nil, fmt.Errorf("inspect report directory descriptor: %w", err)
	}
	if err := validateDirectoryStat(&stat, ownerUID, allowInsecure); err != nil {
		unix.Close(fileDescriptor)
		return nil, err
	}

	return &ReportDirectory{
		fileDescriptor: fileDescriptor,
		ownerUID:       ownerUID,
		allowInsecure:  allowInsecure,
	}, nil
}

// OpenReport opens and validates one current or retained report generation.
func (directory *ReportDirectory) OpenReport(identity ProcessIdentity, rotation int) (*ReportFile, error) {
	if directory == nil {
		return nil, os.ErrClosed
	}
	if identity.PID <= 0 || identity.CreationTime.IsZero() {
		return nil, fmt.Errorf("%w: process identity is incomplete", ErrUnsafeSource)
	}
	name, err := reportName(identity.PID, rotation)
	if err != nil {
		return nil, err
	}

	directory.mutex.RLock()
	defer directory.mutex.RUnlock()
	if directory.fileDescriptor < 0 {
		return nil, os.ErrClosed
	}

	fileDescriptor, err := unix.Openat(
		directory.fileDescriptor,
		name,
		unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW|unix.O_NONBLOCK,
		0,
	)
	if err != nil {
		return nil, fmt.Errorf("open report generation %d: %w", rotation, err)
	}

	var stat unix.Stat_t
	if err := unix.Fstat(fileDescriptor, &stat); err != nil {
		unix.Close(fileDescriptor)
		return nil, fmt.Errorf("inspect report generation %d: %w", rotation, err)
	}
	if err := validateReportStat(&stat, directory.ownerUID, directory.allowInsecure); err != nil {
		unix.Close(fileDescriptor)
		return nil, err
	}

	modificationTime := time.Unix(stat.Mtim.Sec, stat.Mtim.Nsec).UTC()
	if !modificationTime.After(identity.CreationTime) {
		unix.Close(fileDescriptor)
		return nil, fmt.Errorf("%w: generation %d is not newer than the process", ErrStaleGeneration, rotation)
	}

	return &ReportFile{
		file:             os.NewFile(uintptr(fileDescriptor), ""),
		Generation:       GenerationID{Device: uint64(stat.Dev), Inode: stat.Ino},
		Size:             stat.Size,
		ModificationTime: modificationTime,
	}, nil
}

func (directory *ReportDirectory) Close() error {
	if directory == nil {
		return nil
	}

	directory.mutex.Lock()
	defer directory.mutex.Unlock()
	if directory.fileDescriptor < 0 {
		return nil
	}
	err := unix.Close(directory.fileDescriptor)
	directory.fileDescriptor = -1
	return err
}

func openDirectoryNoFollow(path string) (int, error) {
	fileDescriptor, err := unix.Open("/", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return -1, fmt.Errorf("open report directory root: %w", err)
	}

	components := strings.Split(strings.TrimPrefix(path, "/"), "/")
	for index, component := range components {
		if component == "" {
			continue
		}
		nextDescriptor, openErr := unix.Openat(
			fileDescriptor,
			component,
			unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW,
			0,
		)
		unix.Close(fileDescriptor)
		if openErr != nil {
			return -1, fmt.Errorf("open report directory component %d: %w", index+1, openErr)
		}
		fileDescriptor = nextDescriptor
	}
	return fileDescriptor, nil
}

func validateDirectoryStat(stat *unix.Stat_t, expectedUID uint32, allowInsecure bool) error {
	if stat.Mode&unix.S_IFMT != unix.S_IFDIR {
		return fmt.Errorf("%w: report directory descriptor is not a directory", ErrUnsafeSource)
	}
	if allowInsecure {
		return nil
	}
	if stat.Uid != expectedUID {
		return fmt.Errorf("%w: report directory owner does not match the exporter", ErrUnsafeSource)
	}
	if stat.Mode&0o077 != 0 {
		return fmt.Errorf("%w: report directory grants group or other access", ErrUnsafeSource)
	}
	return nil
}

func validateReportStat(stat *unix.Stat_t, expectedUID uint32, allowInsecure bool) error {
	if stat.Mode&unix.S_IFMT != unix.S_IFREG {
		return fmt.Errorf("%w: report descriptor is not a regular file", ErrUnsafeSource)
	}
	if allowInsecure {
		return nil
	}
	if stat.Uid != expectedUID {
		return fmt.Errorf("%w: report owner does not match the exporter", ErrUnsafeSource)
	}
	if stat.Mode&0o077 != 0 {
		return fmt.Errorf("%w: report grants group or other access", ErrUnsafeSource)
	}
	return nil
}

func reportName(pid, rotation int) (string, error) {
	if pid <= 0 {
		return "", fmt.Errorf("pid must be positive")
	}
	if rotation < 0 || rotation > 9 {
		return "", fmt.Errorf("report rotation must be between 0 and 9")
	}

	name := "monitor_" + strconv.Itoa(pid)
	if rotation > 0 {
		name += "_" + strconv.Itoa(rotation)
	}
	return name + ".json", nil
}

var _ io.ReaderAt = (*ReportFile)(nil)
