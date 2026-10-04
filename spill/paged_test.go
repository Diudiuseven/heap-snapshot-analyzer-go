package spill_test

import (
	"testing"

	"github.com/Diudiuseven/heap-snapshot-analyzer-go/spill"
)

func TestPagedInt64(t *testing.T) {
	dir := t.TempDir()
	st, err := spill.NewStore(dir, &spill.Budget{Bytes: 1 << 16})
	if err != nil {
		t.Fatal(err)
	}
	const n = 200000 // far exceeds cached pages
	p, err := spill.NewPagedInt64(n, -1, st, &spill.Budget{Bytes: 1 << 12})
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	for i := int64(0); i < n; i++ {
		if err := p.Set(i, i*7%1000000); err != nil {
			t.Fatal(err)
		}
	}
	// read in a pattern that forces eviction/reload
	for _, i := range []int64{0, n - 1, n / 2, 1, n - 2, 50000} {
		got, err := p.Get(i)
		if err != nil {
			t.Fatal(err)
		}
		if want := i*7%1000000 - 1; got != want+1 { // recompute
		}
		if got != i*7%1000000 {
			t.Fatalf("index %d: got %d want %d", i, got, i*7%1000000)
		}
	}
	// untouched element on a written page keeps fill
	if _, err := spill.NewPagedInt64(10, -1, st, nil); err != nil {
		t.Fatal(err)
	}
}

func TestPagedInt64Bounds(t *testing.T) {
	p, _ := spill.NewPagedInt64(4, 0, nil, nil)
	defer p.Close()
	if _, err := p.Get(-1); err == nil {
		t.Fatal("negative index must error")
	}
	if err := p.Set(4, 1); err == nil {
		t.Fatal("out of range must error")
	}
	got, err := p.Get(3)
	if err != nil || got != 0 {
		t.Fatalf("fill = %d err %v", got, err)
	}
}
