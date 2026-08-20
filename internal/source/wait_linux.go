//go:build linux

package source

import (
	"context"
	"errors"
	"fmt"
	"os"
	"time"

	"golang.org/x/sys/unix"
)

const watcherCancellationPollInterval = 100 * time.Millisecond

// Wait reports whether inotify became readable before maxWait elapsed. It does
// not consume events, so the next reconciliation still observes every flag.
func (watcher *DirectoryWatcher) Wait(ctx context.Context, maxWait time.Duration) (bool, error) {
	if watcher == nil {
		return false, os.ErrClosed
	}
	if ctx == nil {
		return false, fmt.Errorf("watch context is required")
	}
	if maxWait <= 0 {
		return false, fmt.Errorf("watch duration must be positive")
	}

	deadline := time.Now().Add(maxWait)
	for {
		if err := ctx.Err(); err != nil {
			return false, err
		}
		remaining := time.Until(deadline)
		if remaining <= 0 {
			return false, nil
		}
		pollDuration := remaining
		if pollDuration > watcherCancellationPollInterval {
			pollDuration = watcherCancellationPollInterval
		}

		watcher.mutex.Lock()
		if watcher.fileDescriptor < 0 {
			watcher.mutex.Unlock()
			return false, os.ErrClosed
		}
		pollDescriptors := []unix.PollFd{{
			Fd:     int32(watcher.fileDescriptor),
			Events: unix.POLLIN,
		}}
		ready, err := unix.Poll(pollDescriptors, pollMilliseconds(pollDuration))
		watcher.mutex.Unlock()
		if errors.Is(err, unix.EINTR) {
			continue
		}
		if err != nil {
			return false, fmt.Errorf("wait for report directory events: %w", err)
		}
		if ready == 0 {
			continue
		}
		revents := pollDescriptors[0].Revents
		if revents&(unix.POLLERR|unix.POLLHUP|unix.POLLNVAL) != 0 {
			return false, ErrWatcherInvalidated
		}
		if revents&unix.POLLIN != 0 {
			return true, nil
		}
	}
}

func pollMilliseconds(duration time.Duration) int {
	milliseconds := duration.Milliseconds()
	if duration%time.Millisecond != 0 {
		milliseconds++
	}
	if milliseconds < 1 {
		return 1
	}
	return int(milliseconds)
}
