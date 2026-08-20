//go:build linux

package source

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"sync"
	"unsafe"

	"golang.org/x/sys/unix"
)

var ErrWatcherInvalidated = errors.New("report directory watch was invalidated")

const directoryWatchMask = unix.IN_CREATE |
	unix.IN_MOVED_TO |
	unix.IN_MOVED_FROM |
	unix.IN_DELETE |
	unix.IN_MODIFY |
	unix.IN_CLOSE_WRITE |
	unix.IN_ATTRIB |
	unix.IN_MOVE_SELF |
	unix.IN_DELETE_SELF

type DirectoryEvents struct {
	Changed     bool
	Overflowed  bool
	Invalidated bool
}

// DirectoryWatcher turns inotify activity into rescan triggers. Event names
// are deliberately discarded because reports can contain sensitive paths.
type DirectoryWatcher struct {
	mutex           sync.Mutex
	fileDescriptor  int
	watchDescriptor int
}

// WatchReportDirectory registers inotify against the already validated
// directory descriptor, avoiding a second pathname lookup.
func WatchReportDirectory(directory *ReportDirectory) (*DirectoryWatcher, error) {
	if directory == nil {
		return nil, os.ErrClosed
	}

	directory.mutex.RLock()
	if directory.fileDescriptor < 0 {
		directory.mutex.RUnlock()
		return nil, os.ErrClosed
	}
	descriptorPath := "/proc/self/fd/" + strconv.Itoa(directory.fileDescriptor)

	fileDescriptor, err := unix.InotifyInit1(unix.IN_CLOEXEC | unix.IN_NONBLOCK)
	if err != nil {
		directory.mutex.RUnlock()
		return nil, fmt.Errorf("initialize report directory watch: %w", err)
	}
	watchDescriptor, err := unix.InotifyAddWatch(fileDescriptor, descriptorPath, directoryWatchMask)
	directory.mutex.RUnlock()
	if err != nil {
		unix.Close(fileDescriptor)
		return nil, fmt.Errorf("watch report directory descriptor: %w", err)
	}

	return &DirectoryWatcher{
		fileDescriptor:  fileDescriptor,
		watchDescriptor: watchDescriptor,
	}, nil
}

// Drain consumes all currently queued events without blocking.
func (watcher *DirectoryWatcher) Drain() (DirectoryEvents, error) {
	if watcher == nil {
		return DirectoryEvents{}, os.ErrClosed
	}

	watcher.mutex.Lock()
	defer watcher.mutex.Unlock()
	if watcher.fileDescriptor < 0 {
		return DirectoryEvents{}, os.ErrClosed
	}

	var events DirectoryEvents
	buffer := make([]byte, 64*1024)
	for {
		readBytes, err := unix.Read(watcher.fileDescriptor, buffer)
		if errors.Is(err, unix.EINTR) {
			continue
		}
		if errors.Is(err, unix.EAGAIN) {
			return events, nil
		}
		if err != nil {
			return events, fmt.Errorf("read report directory events: %w", err)
		}
		if readBytes == 0 {
			return events, nil
		}
		if err := collectDirectoryEvents(buffer[:readBytes], &events); err != nil {
			return events, err
		}
	}
}

func (watcher *DirectoryWatcher) Close() error {
	if watcher == nil {
		return nil
	}

	watcher.mutex.Lock()
	defer watcher.mutex.Unlock()
	if watcher.fileDescriptor < 0 {
		return nil
	}
	if watcher.watchDescriptor >= 0 {
		_, _ = unix.InotifyRmWatch(watcher.fileDescriptor, uint32(watcher.watchDescriptor))
		watcher.watchDescriptor = -1
	}
	err := unix.Close(watcher.fileDescriptor)
	watcher.fileDescriptor = -1
	return err
}

func collectDirectoryEvents(buffer []byte, events *DirectoryEvents) error {
	headerSize := int(unsafe.Sizeof(unix.InotifyEvent{}))
	for offset := 0; offset < len(buffer); {
		if len(buffer)-offset < headerSize {
			return fmt.Errorf("parse report directory events: truncated event header")
		}

		event := (*unix.InotifyEvent)(unsafe.Pointer(&buffer[offset]))
		eventSize := headerSize + int(event.Len)
		if eventSize < headerSize || eventSize > len(buffer)-offset {
			return fmt.Errorf("parse report directory events: invalid event length")
		}

		events.Changed = true
		if event.Mask&unix.IN_Q_OVERFLOW != 0 {
			events.Overflowed = true
		}
		if event.Mask&(unix.IN_IGNORED|unix.IN_MOVE_SELF|unix.IN_DELETE_SELF) != 0 {
			events.Invalidated = true
		}
		offset += eventSize
	}
	return nil
}
