package file

import (
	"slices"
	"testing"

	"wayseer.dev/sdk"
)

func pts(ts ...int64) []sdk.Point {
	out := make([]sdk.Point, len(ts))
	for i, t := range ts {
		out[i] = sdk.Point{T: t, V: float64(i) + 0.5}
	}
	return out
}

func TestSeriesKeepsEverySampleExactly(t *testing.T) {
	for name, ps := range map[string][]sdk.Point{
		"even":   pts(100, 110, 120, 130),
		"uneven": pts(100, 110, 125, 130),
		"one":    pts(42),
		"none":   nil,
	} {
		s := pack(ps)
		if got := s.within(0, 1000); !slices.Equal(got, ps) && len(ps) > 0 {
			t.Errorf("%s: unpacked %v, want %v", name, got, ps)
		}
		if all := s.all(); len(all) != len(ps) || len(ps) > 0 && !slices.Equal(all, ps) {
			t.Errorf("%s: all %v, want %v", name, all, ps)
		}
		if s.len() != len(ps) {
			t.Errorf("%s: len %d, want %d", name, s.len(), len(ps))
		}
	}
}

func TestEvenSeriesKeepOnlyValues(t *testing.T) {
	if s := pack(pts(100, 110, 120)); s.pts != nil {
		t.Error("evenly spaced samples kept their times")
	}
	if s := pack(pts(100, 110, 125)); s.pts == nil {
		t.Error("unevenly spaced samples lost their times")
	}
}

func TestSeriesWithinIsHalfOpen(t *testing.T) {
	for _, ps := range [][]sdk.Point{pts(100, 110, 120, 130), pts(100, 110, 125, 130)} {
		s := pack(ps)
		for _, w := range [][2]int64{{110, 130}, {105, 126}, {0, 100}, {131, 200}, {100, 101}, {130, 131}} {
			var want []sdk.Point
			for _, p := range ps {
				if p.T >= w[0] && p.T < w[1] {
					want = append(want, p)
				}
			}
			if got := s.within(w[0], w[1]); !slices.Equal(got, want) {
				t.Errorf("%v within %v = %v, want %v", ps, w, got, want)
			}
		}
	}
}

func TestSeriesWithinIsACopy(t *testing.T) {
	s := pack(pts(100, 150, 175))
	got := s.within(0, 1000)
	got[0].T = 1
	if again := s.within(0, 1000); again[0].T != 100 {
		t.Error("changing an answer changed the series")
	}
}
