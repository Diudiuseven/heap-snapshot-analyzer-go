// Package estimate provides unbiased, order-independent estimation
// for sampled heap records.
//
// Determinism contract: each record's estimate is computed with pure
// integer arithmetic (round-half-away-from-zero on a per-record
// basis), and aggregation sums these integers. Integer addition is
// associative and commutative, so the final estimate of a set of
// records is bit-identical under any processing order and any degree
// of concurrency.
package estimate

import "github.com/Diudiuseven/heap-snapshot-analyzer-go/pkg/model"

// Object returns the unbiased estimated (bytes, count) contributed by
// a single (possibly sampled) object record.
//
// For a record sampled with probability p = ScaleDen/ScaleNum, the
// Horvitz–Thompson estimator scales the observed value by 1/p, which
// is unbiased for the population total. ScaleDen <= 0 is treated as
// "unsampled" (scale 1).
func Object(o model.Object) (estBytes int64, estCount int64) {
	num, den := o.ScaleNum, o.ScaleDen
	if num <= 0 {
		num = 1
	}
	if den <= 0 {
		den = 1
	}
	return scale(o.Size, num, den), scale(1, num, den)
}

// scale computes round(v * num / den) with round-half-away-from-zero,
// using only integer math so results are platform- and order-stable.
func scale(v, num, den int64) int64 {
	if num == den {
		return v
	}
	// Use 128-bit-free arithmetic carefully: split to avoid overflow
	// for large v*num. q = v/den, r = v%den; v*num = q*num*den + r*num.
	q := v / den
	r := v % den
	scaled := q * num
	rem := r * num // |r| < den, so |rem| < den*num; callers keep scales small.
	// Fold remainder back with rounding.
	remQ := rem / den
	remR := rem % den
	scaled += remQ
	// Round half away from zero on the remaining fraction remR/den.
	if abs2(remR) >= den {
		if remR >= 0 {
			scaled++
		} else {
			scaled--
		}
	}
	return scaled
}

func abs2(v int64) int64 {
	if v < 0 {
		return -2 * v
	}
	return 2 * v
}
