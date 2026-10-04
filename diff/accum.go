package diff

import (
	"math/big"
	"sort"

	"github.com/Diudiuseven/heap-snapshot-analyzer-go/index"
	"github.com/Diudiuseven/heap-snapshot-analyzer-go/weight"
)

type cellSums struct {
	addedCount, addedBytes, removedCount, removedBytes int64
	retBefore, retAfter                                int64
}

type deltaAccumulator struct {
	addedCnt map[index.CellKey]*big.Rat
	addedBy  map[index.CellKey]*big.Rat
	rmCnt    map[index.CellKey]*big.Rat
	rmBy     map[index.CellKey]*big.Rat
	retBef   map[index.CellKey]*big.Rat
	retAft   map[index.CellKey]*big.Rat
}

func newDeltaAccumulator() *deltaAccumulator {
	return &deltaAccumulator{
		addedCnt: map[index.CellKey]*big.Rat{},
		addedBy:  map[index.CellKey]*big.Rat{},
		rmCnt:    map[index.CellKey]*big.Rat{},
		rmBy:     map[index.CellKey]*big.Rat{},
		retBef:   map[index.CellKey]*big.Rat{},
		retAft:   map[index.CellKey]*big.Rat{},
	}
}

func mul(w weight.Rat, size int64) *big.Rat {
	return new(big.Rat).Mul(w.Big(), big.NewRat(size, 1))
}

func addTo(m map[index.CellKey]*big.Rat, k index.CellKey, v *big.Rat) {
	cur := m[k]
	if cur == nil {
		cur = new(big.Rat)
		m[k] = cur
	}
	cur.Add(cur, v)
}

func addCnt(m map[index.CellKey]*big.Rat, k index.CellKey, w weight.Rat) {
	addTo(m, k, w.Big())
}

func (a *deltaAccumulator) added(k index.CellKey, w weight.Rat, size int64) {
	addCnt(a.addedCnt, k, w)
	addTo(a.addedBy, k, mul(w, size))
}

func (a *deltaAccumulator) removed(k index.CellKey, w weight.Rat, size int64) {
	addCnt(a.rmCnt, k, w)
	addTo(a.rmBy, k, mul(w, size))
}

func (a *deltaAccumulator) retained(k index.CellKey, wb weight.Rat, sb int64, wa weight.Rat, sa int64) {
	addTo(a.retBef, k, mul(wb, sb))
	addTo(a.retAft, k, mul(wa, sa))
}

func roundRat(r *big.Rat) int64 {
	acc := weight.NewAcc()
	if r != nil {
		acc.AddRat(r)
	}
	return acc.RoundInt()
}

func (a *deltaAccumulator) finish(k index.CellKey) cellSums {
	return cellSums{
		addedCount:   roundRat(a.addedCnt[k]),
		addedBytes:   roundRat(a.addedBy[k]),
		removedCount: roundRat(a.rmCnt[k]),
		removedBytes: roundRat(a.rmBy[k]),
		retBefore:    roundRat(a.retBef[k]),
		retAfter:     roundRat(a.retAft[k]),
	}
}

func (a *deltaAccumulator) allKeys() []index.CellKey {
	set := map[index.CellKey]bool{}
	for m := range a.addedCnt {
		set[m] = true
	}
	for m := range a.addedBy {
		set[m] = true
	}
	for m := range a.rmCnt {
		set[m] = true
	}
	for m := range a.rmBy {
		set[m] = true
	}
	for m := range a.retBef {
		set[m] = true
	}
	for m := range a.retAft {
		set[m] = true
	}
	keys := make([]index.CellKey, 0, len(set))
	for k := range set {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		if keys[i].Site != keys[j].Site {
			return keys[i].Site < keys[j].Site
		}
		return keys[i].Type < keys[j].Type
	})
	return keys
}

func deltasKind(obsAdd, obsRm int64, s cellSums) ChangeKind {
	hasRet := s.retBefore != 0 || s.retAfter != 0
	if hasRet {
		return ChangeRetained
	}
	if obsAdd > 0 && obsRm == 0 {
		return ChangeAdded
	}
	if obsRm > 0 && obsAdd == 0 {
		return ChangeRemoved
	}
	return ChangeRetained
}
