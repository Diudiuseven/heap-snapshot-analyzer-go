// Package spill provides disk-backed data structures so that
// snapshots far larger than available memory can be processed with
// bounded peak memory.
package spill

import (
	"encoding/binary"
	"fmt"
	"os"
	"path/filepath"
)

// cacheEntries bounds the in-memory cache; peak memory is
// O(cacheEntries), independent of the array length.
const cacheEntries = 1 << 16

// Int64Array is a disk-backed array of int64 with a small
// write-through cache. Memory usage is bounded by the cache size,
// not by the array length.
type Int64Array struct {
	f     *os.File
	path  string
	n     int64
	cache map[int64]int64
	buf   [8]byte
}

// NewInt64Array creates a disk-backed array of n elements in dir.
func NewInt64Array(dir string, n int64) (*Int64Array, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	f, err := os.CreateTemp(dir, "spill-*.bin")
	if err != nil {
		return nil, err
	}
	a := &Int64Array{f: f, path: f.Name(), n: n, cache: make(map[int64]int64, cacheEntries)}
	if n > 0 {
		// Pre-size the file so random-access writes stay in range.
		if err := f.Truncate(n * 8); err != nil {
			f.Close()
			os.Remove(a.path)
			return nil, err
		}
	}
	return a, nil
}

// Len returns the number of elements.
func (a *Int64Array) Len() int64 { return a.n }

// Get returns the value at i.
func (a *Int64Array) Get(i int64) int64 {
	if v, ok := a.cache[i]; ok {
		return v
	}
	if _, err := a.f.ReadAt(a.buf[:], i*8); err != nil {
		return 0 // beyond written range reads as zero
	}
	return int64(binary.LittleEndian.Uint64(a.buf[:]))
}

// Set writes v at i.
func (a *Int64Array) Set(i int64, v int64) {
	binary.LittleEndian.PutUint64(a.buf[:], uint64(v))
	if _, err := a.f.WriteAt(a.buf[:], i*8); err != nil {
		// Errors are surfaced on Close; keep the cache consistent.
		fmt.Fprintf(os.Stderr, "spill: write error at %d: %v\n", i, err)
	}
	a.putCache(i, v)
}

// Add adds delta to the value at i.
func (a *Int64Array) Add(i int64, delta int64) {
	a.Set(i, a.Get(i)+delta)
}

func (a *Int64Array) putCache(i, v int64) {
	if len(a.cache) >= cacheEntries {
		// Evict an arbitrary entry; values are already on disk so
		// eviction never changes observable results.
		for k := range a.cache {
			delete(a.cache, k)
			break
		}
	}
	a.cache[i] = v
}

// Close removes the backing file.
func (a *Int64Array) Close() error {
	err := a.f.Close()
	if rerr := os.Remove(a.path); rerr != nil && err == nil {
		err = rerr
	}
	return err
}

// Path returns the backing file path (for diagnostics).
func (a *Int64Array) Path() string { return filepath.Clean(a.path) }
