package index

import "github.com/Diudiuseven/heap-snapshot-analyzer-go/weight"

func weightOne(num, den int64) weight.Rat {
	if num == den {
		return weight.One()
	}
	w, err := weight.New(num, den)
	if err != nil {
		return weight.One()
	}
	return w
}
