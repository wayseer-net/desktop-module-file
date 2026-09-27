package file

import (
	"cmp"
	"errors"
	"fmt"
	"io/fs"
	"math"
	"mindseye/pkg/sdk"
	"os"
	"slices"
	"strconv"
	"strings"
	"time"
)

// stamp tells whether a file may have changed since it was read.
type stamp struct {
	exists bool
	size   int64
	mod    int64
}

func statFile(path string) (stamp, error) {
	fi, err := os.Stat(path)
	if err != nil {
		return stamp{}, err
	}
	if !fi.Mode().IsRegular() {
		return stamp{}, errors.New("not a regular file")
	}
	return stamp{exists: true, size: fi.Size(), mod: fi.ModTime().UnixNano()}, nil
}

// mapped is one record's entity and edges.
type mapped struct {
	ent   sdk.Entity
	edges []sdk.Edge
	line  int
}

// loaded is what one file contributed at its last read.
type loaded struct {
	read    bool
	stamp   stamp
	ents    []mapped
	implied []sdk.Entity // bare entities for series ids
	events  []sdk.Event
	points  map[sdk.SeriesRef]series
	raw     map[sdk.SeriesRef][]sdk.Point // points as read, before they are packed
	probs   []problem
	err     error         // the file as a whole could not be used
	scratch []metricPoint // one record's points, reused across records
	sets    attrSets      // attribute maps shared while loading
}

// load reads and maps the file; a file-level failure leaves it contributing nothing.
func (s *source) load(inst sdk.ModuleID) loaded {
	st, err := statFile(s.abs)
	l := loaded{read: true, stamp: st, points: map[sdk.SeriesRef]series{}, raw: map[sdk.SeriesRef][]sdk.Point{}}
	implied := map[sdk.EntityRef]bool{}
	if err == nil {
		var malformed []problem
		malformed, err = s.parse(func(r record) {
			s.mapEntity(inst, r, &l)
			s.mapSeries(inst, r, &l, implied)
			s.mapEvent(inst, r, &l)
		})
		l.probs = append(l.probs, malformed...)
	}
	if err != nil {
		if pe := (*fs.PathError)(nil); errors.As(err, &pe) {
			err = pe.Err // the path is added when reported
		}
		return loaded{read: true, stamp: st, err: err} // nothing mapped before the failure counts
	}
	for ref, ps := range l.raw {
		l.points[ref] = pack(sortPoints(ps))
	}
	l.raw = nil
	l.ents, l.implied = slices.Clone(l.ents), slices.Clone(l.implied) // without append's slack
	l.sets, l.scratch = attrSets{}, nil
	return l
}

// parse reads the file, passing each record to each in order, and returns the malformed ones.
// A CSV file is streamed, so its size is never held at once.
func (s *source) parse(each func(record)) ([]problem, error) {
	if s.Format == "json" {
		src, err := os.ReadFile(s.abs)
		if err != nil {
			return nil, err
		}
		return readJSON(src, s.Records, each)
	}
	f, err := os.Open(s.abs)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	return readCSV(f, s.delim, s.checkColumns, each)
}

// checkColumns rejects a header lacking a field the mapping reads.
func (s *source) checkColumns(cols []string) error {
	for _, f := range s.fields() {
		if !slices.Contains(cols, f) {
			return fmt.Errorf("no column %q (columns: %s)", f, strings.Join(cols, ", "))
		}
	}
	return nil
}

// fields lists every field the mapping reads.
func (s *source) fields() []string {
	var out []string
	if e := s.Entities; e != nil {
		out = append(out, e.ID, e.Name, e.Status, e.Reason, e.Tags)
		out = append(out, e.Attrs...)
		for _, ed := range e.Edges {
			out = append(out, ed.To)
		}
	}
	if m := s.Series; m != nil {
		out = append(out, m.ID, m.Time)
		for _, mm := range m.Metrics {
			out = append(out, mm.Field)
		}
	}
	if m := s.Events; m != nil {
		out = append(out, m.ID, m.Time, m.Severity, m.Type, m.Message)
		out = append(out, m.Fields...)
	}
	return slices.DeleteFunc(out, func(f string) bool { return f == "" })
}

func (s *source) mapEntity(inst sdk.ModuleID, r record, l *loaded) {
	if s.Entities == nil {
		return
	}
	e, edges, err := s.Entities.entity(inst, r, &l.sets)
	if err != nil {
		l.probs = append(l.probs, r.problem("%v", err))
		return
	}
	l.ents = append(l.ents, mapped{ent: e, edges: edges, line: r.line})
}

func (s *source) mapSeries(inst sdk.ModuleID, r record, l *loaded, implied map[sdk.EntityRef]bool) {
	if s.Series == nil {
		return
	}
	e, points, err := s.Series.points(inst, r, l.scratch[:0])
	l.scratch = points
	if err != nil {
		l.probs = append(l.probs, r.problem("%v", err))
		return
	}
	if !implied[e.Ref] {
		implied[e.Ref] = true
		l.implied = append(l.implied, e)
	}
	for _, mp := range points {
		ref := sdk.SeriesRef{Entity: e.Ref, Metric: mp.metric}
		l.raw[ref] = append(l.raw[ref], mp.p)
	}
}

// entity maps one record; any bad field rejects the whole record.
func (m *entityMap) entity(inst sdk.ModuleID, r record, sets *attrSets) (sdk.Entity, []sdk.Edge, error) {
	e, err := bareEntity(inst, m.Kind, m.ID, r)
	if err != nil {
		return e, nil, err
	}
	if name, _ := r.str(m.Name); name != "" {
		e.Name = name
	}
	st, err1 := status(r, m.Status, m.Reason)
	tags, err2 := r.list(m.Tags)
	edges, err3 := m.edges(inst, e.Ref, r)
	if err := errors.Join(err1, err2, err3); err != nil {
		return e, nil, err
	}
	e.Status, e.Tags, e.Attrs = st, tags, sets.attrs(r, m.Attrs)
	return e, edges, nil
}

func bareEntity(inst sdk.ModuleID, kind sdk.Kind, idField string, r record) (sdk.Entity, error) {
	id, err := r.str(idField)
	switch {
	case err != nil:
		return sdk.Entity{}, fmt.Errorf("%s: %w", idField, err)
	case id == "":
		return sdk.Entity{}, fmt.Errorf("empty %s", idField)
	}
	ref, err := sdk.NewEntityRef(string(inst), kind, id)
	if err != nil {
		return sdk.Entity{}, err
	}
	return sdk.Entity{Ref: ref, Kind: kind, Name: id, Source: inst}, nil
}

var statusLevels = map[string]sdk.StatusLevel{
	"": sdk.StatusUnknown, "unknown": sdk.StatusUnknown, "ok": sdk.StatusOK,
	"warn": sdk.StatusWarn, "warning": sdk.StatusWarn,
	"crit": sdk.StatusCrit, "critical": sdk.StatusCrit, "down": sdk.StatusDown,
}

func status(r record, levelField, reasonField string) (sdk.Status, error) {
	s, err := r.str(levelField)
	if err != nil {
		return sdk.Status{}, fmt.Errorf("%s: %w", levelField, err)
	}
	lvl, ok := statusLevels[strings.ToLower(s)]
	if !ok {
		return sdk.Status{}, fmt.Errorf("%s %q (want ok, warn, crit, down or unknown)", levelField, s)
	}
	reason, _ := r.str(reasonField)
	return sdk.Status{Level: lvl, Reason: reason}, nil
}

func attrs(r record, fields []string) map[string]sdk.Value {
	var out map[string]sdk.Value
	for _, f := range fields {
		if v, ok := r.attr(f); ok {
			if out == nil {
				out = make(map[string]sdk.Value, len(fields))
			}
			out[f] = v
		}
	}
	return out
}

func (m *entityMap) edges(inst sdk.ModuleID, from sdk.EntityRef, r record) ([]sdk.Edge, error) {
	var out []sdk.Edge
	for _, ed := range m.Edges {
		targets, err := r.list(ed.To)
		if err != nil {
			return nil, err
		}
		for _, t := range targets {
			to, err := sdk.NewEntityRef(string(inst), ed.Kind, t)
			if err != nil {
				return nil, err
			}
			if to == from {
				return nil, fmt.Errorf("%s: %s relates to itself", ed.To, t)
			}
			out = append(out, sdk.Edge{From: from, To: to, Rel: ed.Rel, Weight: 1, Source: inst})
		}
	}
	return out, nil
}

type metricPoint struct {
	metric string
	p      sdk.Point
}

// points maps one record to its entity and a point per metric present in it, appended to buf.
func (m *seriesMap) points(inst sdk.ModuleID, r record, buf []metricPoint) (sdk.Entity, []metricPoint, error) {
	e, err := bareEntity(inst, m.Kind, m.ID, r)
	if err != nil {
		return e, buf, err
	}
	at, err := timestamp(r, m.Time)
	if err != nil {
		return e, buf, err
	}
	for name, mm := range m.Metrics {
		v, ok, err := r.number(mm.Field)
		if err != nil {
			return e, buf[:0], err
		}
		if ok {
			buf = append(buf, metricPoint{name, sdk.Point{T: at, V: v}})
		}
	}
	return e, buf, nil
}

// timestamp reads field as Unix seconds or RFC 3339, returning Unix nanoseconds.
func timestamp(r record, field string) (int64, error) {
	s, err := r.str(field)
	switch {
	case err != nil:
		return 0, fmt.Errorf("%s: %w", field, err)
	case s == "":
		return 0, fmt.Errorf("empty %s", field)
	}
	if secs, err := strconv.ParseFloat(s, 64); err == nil && !math.IsInf(secs, 0) && !math.IsNaN(secs) {
		whole, frac := math.Modf(secs)
		return time.Unix(int64(whole), int64(math.Round(frac*1e9))).UnixNano(), nil
	}
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		return 0, fmt.Errorf("%s %q is neither RFC 3339 nor Unix seconds", field, s)
	}
	return t.UnixNano(), nil
}

// sortPoints orders points by time; of points at the same time, the last one wins.
func sortPoints(ps []sdk.Point) []sdk.Point {
	slices.SortStableFunc(ps, func(a, b sdk.Point) int { return cmp.Compare(a.T, b.T) })
	out := ps[:0]
	for _, p := range ps {
		if n := len(out); n > 0 && out[n-1].T == p.T {
			out[n-1] = p
			continue
		}
		out = append(out, p)
	}
	return out
}
