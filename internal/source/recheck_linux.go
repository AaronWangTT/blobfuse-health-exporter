//go:build linux

package source

import (
	"errors"
	"fmt"
)

var ErrSourceSessionEnded = errors.New("source process session ended")

type processIdentityLookup func(int) (ProcessIdentity, error)

// VerifyIdentity confirms that the configured PID still identifies the source
// process captured during initialization.
func (session *SourceSession) VerifyIdentity() error {
	if session == nil {
		return ErrSourceSessionEnded
	}
	return verifyProcessIdentity(session.Identity(), ReadProcessIdentity)
}

func verifyProcessIdentity(expected ProcessIdentity, lookup processIdentityLookup) error {
	if lookup == nil {
		return fmt.Errorf("process identity lookup is required")
	}
	current, err := lookup(expected.PID)
	if err != nil {
		return fmt.Errorf("%w: process identity is unavailable: %w", ErrSourceSessionEnded, err)
	}
	if !expected.SameSession(current) {
		return fmt.Errorf("%w: process identity changed", ErrSourceSessionEnded)
	}
	return nil
}
