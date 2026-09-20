package opeval

import (
	"math"
	"sort"
)

func sortInts(v []int) { sort.Ints(v) }

// meanMaxMedian returns the mean, max and median of vals, ignoring NaN.
func meanMaxMedian(vals []float64) (mean, max, median float64) {
	clean := make([]float64, 0, len(vals))
	for _, v := range vals {
		if !math.IsNaN(v) {
			clean = append(clean, v)
		}
	}
	if len(clean) == 0 {
		return math.NaN(), math.NaN(), math.NaN()
	}
	sum := 0.0
	max = math.Inf(-1)
	for _, v := range clean {
		sum += v
		if v > max {
			max = v
		}
	}
	sort.Float64s(clean)
	return sum / float64(len(clean)), max, clean[len(clean)/2]
}

// quantile returns the p-quantile (0..1) of an already-sorted slice.
func quantile(sorted []float64, p float64) float64 {
	if len(sorted) == 0 {
		return math.NaN()
	}
	i := int(p * float64(len(sorted)-1))
	if i < 0 {
		i = 0
	}
	if i >= len(sorted) {
		i = len(sorted) - 1
	}
	return sorted[i]
}
