package spill

import (
	"math/rand"
	"testing"
)

func TestSetGet(t *testing.T) {
	a, err := NewInt64Array(t.TempDir(), 1000)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	a.Set(5, 42)
	a.Add(5, 8)
	if got := a.Get(5); got != 50 {
		t.Fatalf("got %d", got)
	}
	if got := a.Get(999); got != 0 {
		t.Fatalf("unset element not zero: %d", got)
	}
}

// Exceed the cache size to force eviction; values must survive on disk.
func TestEvictionKeepsData(t *testing.T) {
	n := int64(cacheEntries * 4)
	a, err := NewInt64Array(t.TempDir(), n)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	rng := rand.New(rand.NewSource(1))
	want := make([]int64, n)
	for i := int64(0); i < n; i++ {
		v := rng.Int63()
		want[i] = v
		a.Set(i, v)
	}
	// Flush the cache by touching many other elements.
	for i := int64(0); i < n; i++ {
		a.Get(i)
	}
	a.cache = map[int64]int64{} // force cold reads
	for i := int64(0); i < n; i += 97 {
		if got := a.Get(i); got != want[i] {
			t.Fatalf("i=%d got %d want %d", i, got, want[i])
		}
	}
}
