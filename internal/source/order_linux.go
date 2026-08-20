//go:build linux

package source

import "sort"

// OrderGenerationsOldestFirst returns a copy ordered from the oldest retained
// report to the current report. Rotation position is authoritative; remaining
// fields provide deterministic ordering if observations share a position.
func OrderGenerationsOldestFirst(generations []DiscoveredGeneration) []DiscoveredGeneration {
	ordered := append([]DiscoveredGeneration(nil), generations...)
	sort.Slice(ordered, func(left, right int) bool {
		leftGeneration := ordered[left]
		rightGeneration := ordered[right]
		if leftGeneration.Rotation != rightGeneration.Rotation {
			return leftGeneration.Rotation > rightGeneration.Rotation
		}

		leftReport := leftGeneration.Report
		rightReport := rightGeneration.Report
		if leftReport.ModificationTime != rightReport.ModificationTime {
			return leftReport.ModificationTime.Before(rightReport.ModificationTime)
		}
		if leftReport.Generation.Device != rightReport.Generation.Device {
			return leftReport.Generation.Device < rightReport.Generation.Device
		}
		return leftReport.Generation.Inode < rightReport.Generation.Inode
	})
	return ordered
}
