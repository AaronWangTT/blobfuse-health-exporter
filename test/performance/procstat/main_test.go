//go:build linux

package main

import (
	"math"
	"testing"
)

func TestParseProcessTicksHandlesSpacesAndParenthesesInCommand(t *testing.T) {
	stat := []byte("123 (worker (copy)) S 1 2 3 4 5 6 7 8 9 10 120 30 15")
	ticks, err := parseProcessTicks(stat)
	if err != nil {
		t.Fatalf("parseProcessTicks() error = %v", err)
	}
	if ticks != 150 {
		t.Fatalf("parseProcessTicks() = %d, want 150", ticks)
	}
}

func TestParseResidentBytesUsesLinuxKilobytes(t *testing.T) {
	bytes, err := parseResidentBytes([]byte("Name:\ttest\nVmRSS:\t1234 kB\n"))
	if err != nil {
		t.Fatalf("parseResidentBytes() error = %v", err)
	}
	if bytes != 1234*1024 {
		t.Fatalf("parseResidentBytes() = %d, want %d", bytes, 1234*1024)
	}
}

func TestPercentileDoesNotMutateInput(t *testing.T) {
	values := []float64{9, 1, 5, 3}
	if got := percentile(values, 0.50); math.Abs(got-3) > 0.001 {
		t.Fatalf("percentile() = %v, want 3", got)
	}
	if values[0] != 9 || values[1] != 1 {
		t.Fatalf("percentile() mutated input: %v", values)
	}
}
