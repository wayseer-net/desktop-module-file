package file

import (
	"cmp"
	"errors"
	"fmt"
	"maps"
	"mindseye/internal/data"
	"mindseye/internal/model"
	"slices"
	"time"
)

// problemCap bounds the problems reported per file, so one broken file cannot flood the log.
const problemCap = 20

// report is one problem as an event will carry it.
type report struct {
	sev model.Severity
	msg string
}

// state is the world all files describe together.
type state struct {
	ents    map[model.EntityRef]model.Entity
	edges   map[model.EdgeKey]model.Edge
	points  map[data.SeriesRef][]data.Point
	events  []model.Event // oldest first, one per id
	reports []report
	err     error // every file-level failure
}

// combine merges the files in config order; an entity id seen twice keeps its first record.
func combine(srcs []source, files []loaded) state {
	w := state{ents: map[model.EntityRef]model.Entity{}, edges: map[model.EdgeKey]model.Edge{}, points: map[data.SeriesRef][]data.Point{}}
	first := map[model.EntityRef]string{}
	var errs []error
	for i, l := range files {
		path := srcs[i].Path
		if l.err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", path, l.err))
			w.reports = append(w.reports, report{model.SevError, fmt.Sprintf("%s: %v", path, l.err)})
			continue
		}
		probs := slices.Clone(l.probs)
		for _, m := range l.ents {
			if at, dup := first[m.ent.Ref]; dup {
				probs = append(probs, problem{m.line, fmt.Sprintf("duplicate id %q, first at %s", m.ent.Ref.Native(), at)})
				continue
			}
			first[m.ent.Ref] = fmt.Sprintf("%s:%d", path, m.line)
			w.ents[m.ent.Ref] = m.ent
			for _, e := range m.edges {
				w.edges[e.Key()] = e
			}
		}
		w.addPoints(l.points)
		w.events = append(w.events, l.events...)
		w.reports = append(w.reports, reports(path, probs)...)
	}
	for _, l := range files {
		for _, e := range l.implied {
			if _, ok := w.ents[e.Ref]; !ok {
				w.ents[e.Ref] = e
			}
		}
	}
	w.events = uniqueEvents(w.events)
	w.err = errors.Join(errs...)
	return w
}

func (w *state) addPoints(from map[data.SeriesRef][]data.Point) {
	for ref, ps := range from {
		if have, ok := w.points[ref]; ok {
			w.points[ref] = sortPoints(append(slices.Clone(have), ps...))
		} else {
			w.points[ref] = ps
		}
	}
}

// reports orders a file's problems by line and caps them.
func reports(path string, probs []problem) []report {
	slices.SortStableFunc(probs, func(a, b problem) int { return cmp.Compare(a.line, b.line) })
	var out []report
	for i, p := range probs {
		if i == problemCap {
			out = append(out, report{model.SevWarn, fmt.Sprintf("%s: %d more problems not shown", path, len(probs)-i)})
			break
		}
		out = append(out, report{model.SevWarn, fmt.Sprintf("%s:%d: %s", path, p.line, p.msg)})
	}
	return out
}

func sortedKeys[K comparable, V any](m map[K]V, compare func(a, b K) int) []K {
	return slices.SortedFunc(maps.Keys(m), compare)
}

// uniqueEvents orders events by time and keeps the first of each id.
func uniqueEvents(evs []model.Event) []model.Event {
	slices.SortStableFunc(evs, func(a, b model.Event) int { return a.At.Compare(b.At) })
	seen := make(map[string]bool, len(evs))
	return slices.DeleteFunc(evs, func(e model.Event) bool {
		dup := seen[e.ID]
		seen[e.ID] = true
		return dup
	})
}

// newest is the latest sample or event time, false if there are none.
func (w *state) newest() (time.Time, bool) {
	var t int64
	found := false
	for _, ps := range w.points {
		if n := len(ps); n > 0 && (!found || ps[n-1].T > t) {
			t, found = ps[n-1].T, true
		}
	}
	if n := len(w.events); n > 0 && (!found || w.events[n-1].At.UnixNano() > t) {
		t, found = w.events[n-1].At.UnixNano(), true
	}
	return time.Unix(0, t).UTC(), found
}

// move shifts every sample and event by d, copying so the files' loaded times stay as read.
func (w *state) move(d time.Duration) {
	for ref, ps := range w.points {
		moved := make([]data.Point, len(ps))
		for i, p := range ps {
			moved[i] = data.Point{T: p.T + int64(d), V: p.V}
		}
		w.points[ref] = moved
	}
	for i := range w.events {
		w.events[i].At = w.events[i].At.Add(d)
	}
}
