package spill_test

import (
	"errors"
	"fmt"
	"io"
	"math/rand"
	"testing"

	"github.com/Diudiuseven/heap-snapshot-analyzer-go/spill"
)

func runSort(t *testing.T, n int, budget int64) []spill.KV {
	t.Helper()
	dir := t.TempDir()
	st, err := spill.NewStore(dir, &spill.Budget{Bytes: budget})
	if err != nil {
		t.Fatal(err)
	}
	in := make(chan spill.KV)
	go func() {
		defer close(in)
		rng := rand.New(rand.NewSource(1))
		for i := 0; i < n; i++ {
			k := []byte(fmt.Sprintf("k%05d", rng.Intn(n/3+1)))
			in <- spill.KV{Key: k, Val: []byte(fmt.Sprintf("v%d", i)), Seq: int64(i)}
		}
	}()
	it, err := spill.SortMerge(st, in)
	if err != nil {
		t.Fatal(err)
	}
	defer it.Close()
	var got []spill.KV
	for {
		kv, err := it.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		got = append(got, kv)
	}
	if len(got) != n {
		t.Fatalf("got %d records, want %d", len(got), n)
	}
	return got
}

func TestSortMergeSmall(t *testing.T) {
	got := runSort(t, 1000, 4096) // forces many chunks
	for i := 1; i < len(got); i++ {
		if string(got[i-1].Key) > string(got[i].Key) {
			t.Fatalf("not sorted at %d", i)
		}
		if string(got[i-1].Key) == string(got[i].Key) && got[i-1].Seq > got[i].Seq {
			t.Fatalf("equal keys not stable at %d", i)
		}
	}
}

func TestSortMergeSingleAndEmpty(t *testing.T) {
	got := runSort(t, 50, 1<<20) // stays in RAM, one chunk
	if len(got) != 50 {
		t.Fatalf("got %d", len(got))
	}
	dir := t.TempDir()
	st, _ := spill.NewStore(dir, nil)
	in := make(chan spill.KV)
	close(in)
	it, err := spill.SortMerge(st, in)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := it.Next(); !errors.Is(err, io.EOF) {
		t.Fatalf("empty merge must EOF, got %v", err)
	}
}
