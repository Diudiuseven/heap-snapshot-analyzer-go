package retain

import (
	"bufio"
	"encoding/binary"
	"io"
	"os"
	"path/filepath"
)

// diskBucket keeps each semidominator's bucket list in a separate spill file.
// The LT algorithm only ever appends to a bucket and drains it once in
// reverse-DFS order, so append+sequential-read matches the access pattern
// exactly while using O(open fds + read buffer) memory.
//
// File layout: one directory "buckets" with file per key containing raw int64
// (big-endian). An in-memory presence bitmap is not kept; file existence is
// probed lazily.
type diskBucket struct {
	dir  string
	prov pagedArrayProvider
}

func newDiskBucketStore(prov pagedArrayProvider) (bucketStore, error) {
	pp, ok := prov.(*ExternalGraph)
	if !ok {
		return memBucket{}, nil
	}
	dir := filepath.Join(pp.store.Dir(), "buckets")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	return &diskBucket{dir: dir, prov: prov}, nil
}

func (d *diskBucket) path(key int64) string {
	return filepath.Join(d.dir, itoa64(key))
}

func (d *diskBucket) add(key, v int64) {
	f, err := os.OpenFile(d.path(key), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return
	}
	defer f.Close()
	var b [8]byte
	binary.BigEndian.PutUint64(b[:], uint64(v))
	_, _ = f.Write(b[:])
}

func (d *diskBucket) take(key int64) []int64 {
	f, err := os.Open(d.path(key))
	if err != nil {
		return nil
	}
	defer func() {
		f.Close()
		os.Remove(d.path(key))
	}()
	br := bufio.NewReader(f)
	var out []int64
	for {
		var b [8]byte
		_, err := io.ReadFull(br, b[:])
		if err != nil {
			break
		}
		out = append(out, int64(binary.BigEndian.Uint64(b[:])))
	}
	return out
}

func (d *diskBucket) close() error { return os.RemoveAll(d.dir) }

func itoa64(v int64) string {
	if v == 0 {
		return "0"
	}
	neg := v < 0
	if neg {
		v = -v
	}
	var b [20]byte
	p := len(b)
	for v > 0 {
		p--
		b[p] = byte('0' + v%10)
		v /= 10
	}
	if neg {
		p--
		b[p] = '-'
	}
	return string(b[p:])
}
