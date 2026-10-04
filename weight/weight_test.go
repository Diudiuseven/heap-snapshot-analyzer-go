package weight

import (
	"math/big"
	"math/rand"
	"testing"
)

func TestNewValidation(t *testing.T) {
	if _, err := New(1, 0); err == nil {
		t.Fatal("zero denominator must error")
	}
	if _, err := New(1, 2); err == nil {
		t.Fatal("weight < 1 must error")
	}
	w, err := New(4, 1)
	if err != nil || w.IsOne() {
		t.Fatalf("New(4,1) = %v err %v", w, err)
	}
	if w.String() != "4" {
		t.Fatalf("got %q", w.String())
	}
}

func TestRoundHalfAway(t *testing.T) {
	cases := []struct {
		n, d int64
		want int64
	}{
		{1, 2, 1}, {3, 2, 2}, {2, 2, 1}, {-3, 2, -2}, {0, 1, 0}, {5, 3, 2},
	}
	for _, c := range cases {
		a := NewAcc()
		if c.n < c.d {
			a.AddRat(big.NewRat(c.n, c.d))
		} else {
			w, _ := New(c.n, c.d)
			a.AddWeighted(w, 1)
		}
		if got := a.RoundInt(); got != c.want {
			t.Errorf("%d/%d = %d, want %d", c.n, c.d, got, c.want)
		}
	}
}

// TestOrderIndependence is the core unbiased-estimate invariant: the rounded
// total of weighted counts is identical for every permutation and sharding.
func TestOrderIndependence(t *testing.T) {
	type wv struct{ n, d, cnt int64 }
	vals := []wv{}
	rng := rand.New(rand.NewSource(42))
	for i := 0; i < 200; i++ {
		d := int64(1 + rng.Intn(7))
		n := d + int64(rng.Intn(10))
		vals = append(vals, wv{n, d, int64(1 + rng.Intn(3))})
	}

	reference := NewAcc()
	for _, v := range vals {
		w, _ := New(v.n, v.d)
		reference.AddWeighted(w, v.cnt)
	}
	want := reference.RoundInt()

	for trial := 0; trial < 50; trial++ {
		perm := rng.Perm(len(vals))
		shard := []*Acc{NewAcc(), NewAcc(), NewAcc(), NewAcc()}
		for i, pi := range perm {
			v := vals[pi]
			w, _ := New(v.n, v.d)
			shard[i%4].AddWeighted(w, v.cnt)
		}
		total := NewAcc()
		for _, s := range shard {
			total.AddRat(s.Rat())
		}
		if got := total.RoundInt(); got != want {
			t.Fatalf("trial %d: %d != %d", trial, got, want)
		}
	}
}
