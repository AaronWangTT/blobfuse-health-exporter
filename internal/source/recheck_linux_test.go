//go:build linux

package source

import (
	"errors"
	"os"
	"testing"
)

func TestVerifyProcessIdentityAcceptsSameSession(t *testing.T) {
	expected := testIdentity()
	err := verifyProcessIdentity(expected, func(pid int) (ProcessIdentity, error) {
		if pid != expected.PID {
			t.Fatalf("lookup pid = %d, want %d", pid, expected.PID)
		}
		return expected, nil
	})
	if err != nil {
		t.Fatalf("verifyProcessIdentity() error = %v", err)
	}
}

func TestVerifyProcessIdentityRejectsChangedSession(t *testing.T) {
	expected := testIdentity()
	tests := []struct {
		name    string
		current ProcessIdentity
	}{
		{name: "boot ID", current: withBootID(expected, "11111111-2222-3333-4444-555555555555")},
		{name: "PID", current: withPID(expected, expected.PID+1)},
		{name: "start ticks", current: withStartTicks(expected, expected.StartTicks+1)},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := verifyProcessIdentity(expected, func(int) (ProcessIdentity, error) {
				return test.current, nil
			})
			if !errors.Is(err, ErrSourceSessionEnded) {
				t.Fatalf("verifyProcessIdentity() error = %v, want ErrSourceSessionEnded", err)
			}
		})
	}
}

func TestVerifyProcessIdentityRejectsUnavailableProcess(t *testing.T) {
	err := verifyProcessIdentity(testIdentity(), func(int) (ProcessIdentity, error) {
		return ProcessIdentity{}, os.ErrNotExist
	})
	if !errors.Is(err, ErrSourceSessionEnded) || !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("verifyProcessIdentity() error = %v, want session-ended and not-exist errors", err)
	}
}

func withBootID(identity ProcessIdentity, bootID string) ProcessIdentity {
	identity.BootID = bootID
	return identity
}

func withPID(identity ProcessIdentity, pid int) ProcessIdentity {
	identity.PID = pid
	return identity
}

func withStartTicks(identity ProcessIdentity, startTicks uint64) ProcessIdentity {
	identity.StartTicks = startTicks
	return identity
}
