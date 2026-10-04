package estimate

import (
	"math/rand"
	"testing"

	"github.com/Diudiuseven/heap-snapshot-analyzer-go/pkg/model"
)

func TestUnsampledIdentity(t *testing.T) {
	b, c := Object(model.Object{Size: 128})
	if b != 128 || c != 1 {
		t.Fatalf("unsampled: got (%d,%d)", b, c)
	}
}

func TestIntegerScale(t *testing.T) {
	b, c := Object(model.Object{Size: 64, ScaleNum: 4, ScaleDen: 1})
	if b != 256 || c != 4 {
		t.Fatalf("got (%d,%d)", b, c)
	}
}

func TestFractionalScaleRounding(t *testing.T) {
	// 1/p = 3/2: 10 bytes -> 15
	b, _ := Object(model.Object{Size: 10, ScaleNum: 3, ScaleDen: 2})
	if b != 15 {
		t.Fatalf("got %d", b)
	}
	// half rounds away from zero: 5 * 3/2 = 7.5 -> 8
	b, _ = Object(model.Object{Size: 5, ScaleNum: 3, ScaleDen: 2})
	if b != 8 {
		t.Fatalf("got %d", b)
	}
}

// The estimate of a set must be identical under any processing order.
func TestOrderIndependence(t *testing.T) {
	rng := rand.New(rand.NewSource(42))
	var objs []model.Object
	for i := 0; i < 1000; i++ {
		objs = append(objs, model.Object{
			Size:     int64(rng.Intn(4096)),
			ScaleNum: int64(1 + rng.Intn(16)),
			ScaleDen: int64(1 + rng.Intn(16)),
		})
	}
	sum := func(os []model.Object) int64 {
		var s int64
		for _, o := range os {
			b, _ := Object(o)
			s += b
		}
		return s
	}
	want := sum(objs)
	for trial := 0; trial < 20; trial++ {
		shuffled := append([]model.Object(nil), objs...)
		rng.Shuffle(len(shuffled), func(i, j int) { shuffled[i], shuffled[j] = shuffled[j], shuffled[i] })
		if got := sum(shuffled); got != want {
			t.Fatalf("order changed result: %d != %d", got, want)
		}
	}
}

// Unbiasedness: expectation of the HT estimator tracks the true total.
func TestUnbiasedExpectation(t *testing.T) {
	// Population: 100 objects of 100 bytes each; sample each with p=1/4.
	// True total = 10000. Mean estimate over trials should be close.
	const trials = 2000
	var total int64
	rng := rand.New(rand.NewSource(7))
	for tr := 0; tr < trials; tr++ {
		var est int64
		for i := 0; i < 100; i++ {
			if rng.Intn(4) == 0 {
				b, _ := Object(model.Object{Size: 100, ScaleNum: 4, ScaleDen: 1})
				est += b
			}
		}
		total += est
	}
	mean := float64(total) / trials
	if mean < 9500 || mean > 10500 {
		t.Fatalf("mean estimate %f far from true 10000", mean)
	}
}
