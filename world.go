package file

import (
	"cmp"
	"errors"
	"fmt"
	"maps"
	"slices"
	"time"
	"wayseer/pkg/sdk"
)

// problemCap bounds the problems reported per file, so one broken file cannot flood the log.
const problemCap = 20

// report is one problem as an event will carry it.
type report struct {
	sev sdk.Severity
	msg string
}

// state is the world all files describe together.
type state struct {
	ents    map[sdk.EntityRef]*sdk.Entity // into the files' loaded entities, which stay as read
	edges   map[sdk.EdgeKey]*sdk.Edge
	points  map[sdk.SeriesRef]series
	events  []sdk.Event // oldest first, one per id
	reports []report
	err     error // every file-level failure
}

// combine merges the files in config order; an entity id seen twice keeps its first record.
func combine(srcs []source, files []loaded) state {
	w := state{ents: map[sdk.EntityRef]*sdk.Entity{}, edges: map[sdk.EdgeKey]*sdk.Edge{}, points: map[sdk.SeriesRef]series{}}
	first := map[sdk.EntityRef]string{}
	var errs []error
	for i, l := range files {
		path := srcs[i].Path
		if l.err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", path, l.err))
			w.reports = append(w.reports, report{sdk.SevError, fmt.Sprintf("%s: %v", path, l.err)})
			continue
		}
		probs := slices.Clone(l.probs)
		for j := range l.ents {
			m := &l.ents[j]
			if at, dup := first[m.ent.Ref]; dup {
				probs = append(probs, problem{m.line, fmt.Sprintf("duplicate id %q, first at %s", m.ent.Ref.Native(), at)})
				continue
			}
			first[m.ent.Ref] = fmt.Sprintf("%s:%d", path, m.line)
			w.ents[m.ent.Ref] = &m.ent
			for k := range m.edges {
				w.edges[m.edges[k].Key()] = &m.edges[k]
			}
		}
		w.addPoints(l.points)
		w.events = append(w.events, l.events...)
		w.reports = append(w.reports, reports(path, probs)...)
	}
	for _, l := range files {
		for j := range l.implied {
			if e := &l.implied[j]; w.ents[e.Ref] == nil {
				w.ents[e.Ref] = e
			}
		}
	}
	w.events = uniqueEvents(w.events)
	w.err = errors.Join(errs...)
	return w
}

// addPoints adds a file's series; a series in two files merges, the later file's sample
// winning at the same time.
func (w *state) addPoints(from map[sdk.SeriesRef]series) {
	for ref, s := range from {
		if have, ok := w.points[ref]; ok {
			w.points[ref] = pack(sortPoints(append(have.all(), s.all()...)))
		} else {
			w.points[ref] = s
		}
	}
}

// reports orders a file's problems by line and caps them.
func reports(path string, probs []problem) []report {
	slices.SortStableFunc(probs, func(a, b problem) int { return cmp.Compare(a.line, b.line) })
	var out []report
	for i, p := range probs {
		if i == problemCap {
			out = append(out, report{sdk.SevWarn, fmt.Sprintf("%s: %d more problems not shown", path, len(probs)-i)})
			break
		}
		out = append(out, report{sdk.SevWarn, fmt.Sprintf("%s:%d: %s", path, p.line, p.msg)})
	}
	return out
}

func sortedKeys[K comparable, V any](m map[K]V, compare func(a, b K) int) []K {
	return slices.SortedFunc(maps.Keys(m), compare)
}

// uniqueEvents orders events by time and keeps the first of each id.
func uniqueEvents(evs []sdk.Event) []sdk.Event {
	slices.SortStableFunc(evs, func(a, b sdk.Event) int { return a.At.Compare(b.At) })
	seen := make(map[string]bool, len(evs))
	return slices.DeleteFunc(evs, func(e sdk.Event) bool {
		dup := seen[e.ID]
		seen[e.ID] = true
		return dup
	})
}

// newest is the latest sample or event time, false if there are none.
func (w *state) newest() (time.Time, bool) {
	var t int64
	found := false
	for _, s := range w.points {
		if last, ok := s.last(); ok && (!found || last > t) {
			t, found = last, true
		}
	}
	if n := len(w.events); n > 0 && (!found || w.events[n-1].At.UnixNano() > t) {
		t, found = w.events[n-1].At.UnixNano(), true
	}
	return time.Unix(0, t).UTC(), found
}

// moveEvents shifts every event by d; samples stay as read, and queries move them instead.
func (w *state) moveEvents(d time.Duration) {
	for i := range w.events {
		w.events[i].At = w.events[i].At.Add(d)
	}
}
