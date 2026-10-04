package index

import (
	"bytes"
	"encoding/gob"
	"math/big"
)

// gobRat serializes an exact rational so partial aggregates can be stored on
// disk and merged later without losing precision.
func gobRat(r *big.Rat) ([]byte, error) {
	if r == nil {
		return nil, nil
	}
	type pair struct{ N, D []byte }
	p := pair{N: r.Num().Bytes(), D: r.Denom().Bytes()}
	var buf bytes.Buffer
	if err := gob.NewEncoder(&buf).Encode(p); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// ungobRat decodes a gob-encoded rational.
func ungobRat(b []byte) (*big.Rat, error) {
	if len(b) == 0 {
		return new(big.Rat), nil
	}
	type pair struct{ N, D []byte }
	var p pair
	if err := gob.NewDecoder(bytes.NewReader(b)).Decode(&p); err != nil {
		return nil, err
	}
	n := new(big.Int).SetBytes(p.N)
	d := new(big.Int).SetBytes(p.D)
	if d.Sign() == 0 {
		return nil, nil
	}
	return new(big.Rat).SetFrac(n, d), nil
}
