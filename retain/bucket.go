package retain

// bucketStore holds Lengauer-Tarjan bucket lists keyed by semidominator DFS
// number. add appends v to bucket[key]; take removes and returns the list.
type bucketStore interface {
	add(key, v int64)
	take(key int64) []int64
	close() error
}

// memBucket is the RAM implementation.
type memBucket struct{ b [][]int64 }

func (m memBucket) add(key, v int64) { m.b[key] = append(m.b[key], v) }
func (m memBucket) take(key int64) []int64 {
	out := m.b[key]
	m.b[key] = nil
	return out
}
func (m memBucket) close() error { return nil }
