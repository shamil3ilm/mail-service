package metrics

import "math"

// Kept in its own file so we can swap to a mutex-based fallback if we
// ever need to compile against a target where math.Float64bits misbehaves.
func floatToBitsImpl(v float64) uint64 { return math.Float64bits(v) }
func bitsToFloatImpl(b uint64) float64 { return math.Float64frombits(b) }
