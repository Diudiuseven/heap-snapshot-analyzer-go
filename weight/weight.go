// Package weight provides order-independent, exact-rational arithmetic for
// sampling weights.
//
// Every sampled object is inflated by its inverse inclusion probability
// w >= 1. Aggregates keep sums as exact rationals and round exactly once, at
// the boundary, using deterministic "round half away from zero". The
// estimate is therefore identical regardless of record order or
// concurrency: rational addition is commutative and associative, and each
// output cell is rounded independently, so any grouping/order of partial
// sums yields the same integer.
package weight

import (
	"errors"
	"math"
	"math/big"
)

// Rat is an immutable non-negative rational number. Its zero value denotes 1
// (the weight of an enumerated, non-sampled object) so that plain
// unweighted code paths need no allocation.
type Rat struct {
	r *big.Rat // nil means 1
}

// One returns the weight of an enumerated (unsampled) object.
func One() Rat { return Rat{} }

// New returns n/d. It rejects d <= 0 and weights < 1 (which cannot be
// inverse inclusion probabilities).
func New(n, d int64) (Rat, error) {
	if d <= 0 {
		return Rat{}, errors.New("weight: non-positive denominator")
	}
	if n < d {
		return Rat{}, errors.New("weight: inverse inclusion probability must be >= 1")
	}
	r := new(big.Rat).SetFrac(big.NewInt(n), big.NewInt(d))
	if n == d {
		return Rat{}, nil // canonicalize 1 to the zero value
	}
	return Rat{r: r}, nil
}

// FromRat wraps an exact big.Rat. The caller must not mutate v afterwards.
// Values <= 0 are rejected; 1 is canonicalized.
func FromRat(v *big.Rat) (Rat, error) {
	if v == nil {
		return Rat{}, nil
	}
	if v.Sign() <= 0 {
		return Rat{}, errors.New("weight: non-positive weight")
	}
	if v.Cmp(big.NewRat(1, 1)) == 0 {
		return Rat{}, nil
	}
	cp := new(big.Rat).Set(v)
	return Rat{r: cp}, nil
}

// Validate checks the weight is a finite rational >= 1.
func (x Rat) Validate() error {
	if x.r == nil {
		return nil
	}
	if x.r.Sign() <= 0 {
		return errors.New("weight: non-positive weight")
	}
	if x.r.Cmp(big.NewRat(1, 1)) < 0 {
		return errors.New("weight: weight below 1")
	}
	return nil
}

// IsOne reports whether x equals 1.
func (x Rat) IsOne() bool { return x.r == nil || x.r.Cmp(big.NewRat(1, 1)) == 0 }

// Sign returns -1, 0 or 1. The zero value (1) is positive.
func (x Rat) Sign() int {
	if x.r == nil {
		return 1
	}
	return x.r.Sign()
}

// Cmp returns -1/0/1 comparing x and y.
func (x Rat) Cmp(y Rat) int { return x.asBig().Cmp(y.asBig()) }

// String renders x as an exact fraction or integer.
func (x Rat) String() string { return x.asBig().RatString() }

// FloatString renders a decimal approximation with prec digits after the
// point. Display only; reported estimates use RoundInt.
func (x Rat) FloatString(prec int) string { return x.asBig().FloatString(prec) }

// asBig returns the underlying rational, treating the zero value as 1.
func (x Rat) asBig() *big.Rat {
	if x.r == nil {
		return big.NewRat(1, 1)
	}
	return x.r
}

// Big returns a copy of the underlying exact rational.
func (x Rat) Big() *big.Rat { return new(big.Rat).Set(x.asBig()) }

// RoundInt returns x rounded half-away-from-zero to an int64.
func (x Rat) RoundInt() int64 {
	a := NewAcc()
	a.AddRat(x.asBig())
	return a.RoundInt()
}

// MulInt returns x*n.
func (x Rat) MulInt(n int64) Rat {
	if n == 1 {
		return x
	}
	r := new(big.Rat).Mul(x.asBig(), big.NewRat(n, 1))
	v, err := FromRat(r)
	if err != nil {
		// x >= 1 and n >= 0 keep this unreachable for valid n.
		return Rat{}
	}
	return v
}

// Acc accumulates exact-rational weighted byte/count sums.
//
// Acc is not safe for concurrent use; the index package serializes updates
// (or merges per-shard accumulators, which is exact).
type Acc struct {
	r *big.Rat
}

// NewAcc returns a zero accumulator.
func NewAcc() *Acc { return &Acc{r: new(big.Rat)} }

// Add adds an unweighted unit (weight 1) n times.
func (a *Acc) Add(n int64) {
	if a.r == nil {
		a.r = new(big.Rat)
	}
	a.r.Add(a.r, big.NewRat(n, 1))
}

// AddWeighted adds w*n exactly.
func (a *Acc) AddWeighted(w Rat, n int64) {
	if a.r == nil {
		a.r = new(big.Rat)
	}
	term := new(big.Rat).Mul(w.asBig(), big.NewRat(n, 1))
	a.r.Add(a.r, term)
}

// AddRat folds another exact rational in (used when merging partial sums).
func (a *Acc) AddRat(r *big.Rat) {
	if r == nil {
		return
	}
	if a.r == nil {
		a.r = new(big.Rat)
	}
	a.r.Add(a.r, r)
}

// Rat returns a copy of the exact accumulated value.
func (a *Acc) Rat() *big.Rat {
	if a.r == nil {
		return new(big.Rat)
	}
	return new(big.Rat).Set(a.r)
}

// RoundInt returns the exact sum rounded half-away-from-zero to an int64.
// Rounding happens here and nowhere else; internal aggregates stay exact.
// Returns math.MaxInt64/MinInt64 on overflow rather than silently wrapping.
func (a *Acc) RoundInt() int64 {
	if a.r == nil || a.r.Sign() == 0 {
		return 0
	}
	return roundHalfAway(a.r)
}

// roundHalfAway rounds r to the nearest integer, ties away from zero.
func roundHalfAway(r *big.Rat) int64 {
	num := new(big.Int).Set(r.Num())
	den := new(big.Int).Set(r.Denom())
	neg := num.Sign() < 0
	if neg {
		num.Neg(num)
	}
	q := new(big.Int).Quo(num, den)
	rem := new(big.Int).Mod(num, den)
	twice := new(big.Int).Lsh(rem, 1)
	if twice.Cmp(den) >= 0 { // rem*2 >= den  -> round up (away from zero)
		q.Add(q, big.NewInt(1))
	}
	if neg {
		q.Neg(q)
	}
	if q.IsInt64() {
		return q.Int64()
	}
	if neg {
		return math.MinInt64
	}
	return math.MaxInt64
}
