//go:build linux

package source

import (
	"reflect"
	"testing"
	"time"
)

func TestOrderGenerationsOldestFirst(t *testing.T) {
	baseTime := time.Unix(1_700_000_000, 0).UTC()
	generations := []DiscoveredGeneration{
		discoveredForOrder(0, 4, 40, baseTime.Add(4*time.Second)),
		discoveredForOrder(9, 1, 10, baseTime.Add(time.Second)),
		discoveredForOrder(3, 3, 30, baseTime.Add(3*time.Second)),
		discoveredForOrder(7, 2, 20, baseTime.Add(2*time.Second)),
	}
	original := append([]DiscoveredGeneration(nil), generations...)

	ordered := OrderGenerationsOldestFirst(generations)
	got := make([]int, 0, len(ordered))
	for _, generation := range ordered {
		got = append(got, generation.Rotation)
	}
	if want := []int{9, 7, 3, 0}; !reflect.DeepEqual(got, want) {
		t.Fatalf("rotations = %v, want %v", got, want)
	}
	if !reflect.DeepEqual(generations, original) {
		t.Fatal("OrderGenerationsOldestFirst() mutated its input")
	}
}

func TestOrderGenerationsUsesDeterministicTieBreakers(t *testing.T) {
	baseTime := time.Unix(1_700_000_000, 0).UTC()
	generations := []DiscoveredGeneration{
		discoveredForOrder(4, 2, 10, baseTime),
		discoveredForOrder(4, 1, 20, baseTime),
		discoveredForOrder(4, 1, 10, baseTime.Add(time.Second)),
		discoveredForOrder(4, 1, 10, baseTime),
	}

	ordered := OrderGenerationsOldestFirst(generations)
	got := make([]GenerationID, 0, len(ordered))
	for _, generation := range ordered {
		got = append(got, generation.Report.Generation)
	}
	want := []GenerationID{
		{Device: 1, Inode: 10},
		{Device: 1, Inode: 20},
		{Device: 2, Inode: 10},
		{Device: 1, Inode: 10},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("generation IDs = %#v, want %#v", got, want)
	}
}

func discoveredForOrder(rotation int, device, inode uint64, modificationTime time.Time) DiscoveredGeneration {
	return DiscoveredGeneration{
		Rotation: rotation,
		Report: &ReportFile{
			Generation:       GenerationID{Device: device, Inode: inode},
			ModificationTime: modificationTime,
		},
	}
}
