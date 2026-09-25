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
		w.reports = append(w.reports, reports(path, probs)...)
	}
	for _, l := range files {
		for _, e := range l.implied {
			if _, ok := w.ents[e.Ref]; !ok {
				w.ents[e.Ref] = e
			}
		}
	}
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

// changes turns the difference between what was sent and w into a change set, and records w
// as sent.
func (m *Module) changes(now time.Time) *model.ChangeSet {
	w := &m.world
	cs := &model.ChangeSet{}
	for _, r := range sortedKeys(m.sent, cmp.Compare) {
		if _, ok := w.ents[r]; !ok {
			cs.Removes = append(cs.Removes, r)
			delete(m.sent, r)
		}
	}
	for _, k := range sortedKeys(m.sentEdges, compareEdgeKeys) {
		if _, ok := w.edges[k]; !ok {
			cs.RemoveEdges = append(cs.RemoveEdges, k)
			delete(m.sentEdges, k)
		}
	}
	for _, r := range sortedKeys(w.ents, cmp.Compare) {
		e := w.ents[r]
		if old, ok := m.sent[r]; !ok || !sameEntity(&old, &e) {
			m.sent[r] = e
			e.Seen = now
			cs.Upserts = append(cs.Upserts, e)
		}
	}
	for _, k := range sortedKeys(w.edges, compareEdgeKeys) {
		if _, ok := m.sentEdges[k]; !ok {
			m.sentEdges[k] = w.edges[k]
			cs.Edges = append(cs.Edges, w.edges[k])
		}
	}
	return cs
}

func sameEntity(a, b *model.Entity) bool {
	return a.Name == b.Name && a.Status == b.Status && slices.Equal(a.Tags, b.Tags) &&
		maps.EqualFunc(a.Attrs, b.Attrs, model.Value.Equal)
}

func sortedKeys[K comparable, V any](m map[K]V, compare func(a, b K) int) []K {
	return slices.SortedFunc(maps.Keys(m), compare)
}

func compareEdgeKeys(a, b model.EdgeKey) int {
	return cmp.Or(cmp.Compare(a.From, b.From), cmp.Compare(a.To, b.To), cmp.Compare(a.Rel, b.Rel))
}
