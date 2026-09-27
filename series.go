package file

import (
	"cmp"
	"mindseye/pkg/sdk"
	"slices"
)

// series is one series' samples in time order. Evenly spaced samples, as recordings and
// exports usually are, keep only their values: half the memory, and nothing lost.
type series struct {
	start, step int64       // the first time and the spacing, while pts is nil
	vals        []float64   // evenly spaced values
	pts         []sdk.Point // unevenly spaced samples
}

// pack stores sorted, distinct-time points in as little memory as they allow.
func pack(ps []sdk.Point) series {
	if !even(ps) {
		return series{pts: slices.Clone(ps)}
	}
	s := series{vals: make([]float64, len(ps))}
	for i, p := range ps {
		s.vals[i] = p.V
	}
	if len(ps) > 0 {
		s.start = ps[0].T
	}
	if len(ps) > 1 {
		s.step = ps[1].T - ps[0].T
	}
	return s
}

func even(ps []sdk.Point) bool {
	for i := 2; i < len(ps); i++ {
		if ps[i].T-ps[i-1].T != ps[1].T-ps[0].T {
			return false
		}
	}
	return true
}

func (s *series) len() int {
	if s.pts != nil {
		return len(s.pts)
	}
	return len(s.vals)
}

// within returns a copy of the samples at or after from and before to.
func (s *series) within(from, to int64) []sdk.Point {
	lo, hi := s.index(from), s.index(to)
	if s.pts != nil {
		return slices.Clone(s.pts[lo:hi])
	}
	out := make([]sdk.Point, hi-lo)
	for i := range out {
		out[i] = sdk.Point{T: s.start + int64(lo+i)*s.step, V: s.vals[lo+i]}
	}
	return out
}

// index is the position of the first sample at or after t.
func (s *series) index(t int64) int {
	if s.pts != nil {
		i, _ := slices.BinarySearchFunc(s.pts, t, func(p sdk.Point, t int64) int { return cmp.Compare(p.T, t) })
		return i
	}
	n := int64(len(s.vals))
	switch {
	case n == 0 || t <= s.start:
		return 0
	case s.step == 0:
		return int(n) // one sample, before t
	}
	i := (t - s.start + s.step - 1) / s.step // rounds up to the first sample not before t
	return int(min(i, n))
}

// last is the time of the newest sample, false if there are none.
func (s *series) last() (int64, bool) {
	if n := s.len(); n > 0 {
		if s.pts != nil {
			return s.pts[n-1].T, true
		}
		return s.start + int64(n-1)*s.step, true
	}
	return 0, false
}

// all returns a copy of every sample.
func (s *series) all() []sdk.Point {
	if s.pts != nil {
		return slices.Clone(s.pts)
	}
	return s.within(s.start, s.start+int64(len(s.vals))*s.step+1)
}
