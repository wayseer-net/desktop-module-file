package file

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"mindseye/internal/data"
	"mindseye/internal/model"
	"mindseye/internal/module"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/fsnotify/fsnotify"
)

// Kind is the module kind in config.
const Kind = "file"

const (
	version  = "1"
	eventCap = 1000                  // problem events kept for queries
	settle   = 50 * time.Millisecond // quiet time after a file event before reloading
)

func init() { module.Register(Kind, func() module.Module { return New() }) }

// Module maps CSV and JSON files to entities, edges and series.
type Module struct {
	name      model.ModuleID
	opts      options
	paths     map[string]bool // absolute paths of the files, for matching watcher events
	catalogue []module.Metric
	health    atomic.Pointer[data.Health]

	mu       sync.Mutex // guards what follows, shared by Run and queries
	files    []loaded   // parallel to opts.Files
	world    state
	tracker  module.Tracker
	reported map[string]bool // problems already sent as events
	events   *module.EventLog
	seq      uint64 // events sent so far
}

// New makes an unconfigured module.
func New() *Module { return &Module{} }

// Info describes the module.
func (m *Module) Info() module.Info {
	return module.Info{Kind: Kind, Version: version, Description: "Entities, edges and series from CSV and JSON files"}
}

// Configure decodes and checks the mapping; files are read by Run and Discover.
func (m *Module) Configure(_ context.Context, cfg module.Config) error {
	o := options{Rescan: 5 * time.Second}
	if err := cfg.Decode(&o); err != nil {
		return err
	}
	if err := o.prepare(); err != nil {
		return fmt.Errorf("line %d: %w", cfg.Line, err)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.name, m.opts = cfg.Name, o
	m.paths = map[string]bool{}
	for _, s := range o.Files {
		m.paths[s.abs] = true
	}
	m.catalogue = catalogue(o.Files)
	m.files = make([]loaded, len(o.Files))
	m.world = state{}
	m.tracker.Reset()
	m.reported, m.events, m.seq = map[string]bool{}, module.NewEventLog(eventCap), 0
	m.health.Store(&data.Health{})
	return nil
}

// catalogue lists each mapped metric with the kinds that have it.
func catalogue(srcs []source) []module.Metric {
	byName := map[string]*module.Metric{}
	for _, s := range srcs {
		if s.Series == nil {
			continue
		}
		for name, mm := range s.Series.Metrics {
			c := byName[name]
			if c == nil {
				c = &module.Metric{Name: name, Unit: mm.Unit, Description: "read from " + s.Path, Native: mm.Field}
				byName[name] = c
			}
			if !slices.Contains(c.Kinds, s.Series.Kind) {
				c.Kinds = append(c.Kinds, s.Series.Kind)
			}
		}
	}
	out := make([]module.Metric, 0, len(byName))
	for _, c := range byName {
		out = append(out, *c)
	}
	slices.SortFunc(out, func(a, b module.Metric) int { return cmp.Compare(a.Name, b.Name) })
	return out
}

// Run sends a snapshot, then a delta whenever a file changes, until ctx ends.
func (m *Module) Run(ctx context.Context, sink module.Sink) error {
	w, err := fsnotify.NewWatcher()
	if err != nil {
		w = nil // the rescan still notices changes, only later
	} else {
		defer func() { _ = w.Close() }()
	}
	m.watchDirs(w)
	m.mu.Lock()
	m.tracker.Reset() // a snapshot resends everything
	m.mu.Unlock()
	if err := sink.Snapshot(ctx, m.refresh(time.Now(), nil)); err != nil {
		return err
	}
	return m.follow(ctx, sink, w)
}

// follow sends deltas as files change: soon after a watcher event, and at every rescan.
func (m *Module) follow(ctx context.Context, sink module.Sink, w *fsnotify.Watcher) error {
	var events <-chan fsnotify.Event
	var errs <-chan error
	if w != nil {
		events, errs = w.Events, w.Errors
	}
	settled := time.NewTimer(settle)
	settled.Stop()
	rescan := time.NewTicker(m.opts.Rescan)
	defer rescan.Stop()
	touched := map[string]bool{}
	for {
		var cs *model.ChangeSet
		select {
		case <-ctx.Done():
			return nil
		case ev, ok := <-events:
			if !ok {
				events = nil
			} else if name := filepath.Clean(ev.Name); m.paths[name] {
				touched[name] = true
				settled.Reset(settle)
			}
			continue
		case _, ok := <-errs:
			if !ok {
				errs = nil
			}
			continue // an overflow or watch error is caught by the next rescan
		case now := <-settled.C:
			cs, touched = m.refresh(now, touched), map[string]bool{}
		case now := <-rescan.C:
			m.watchDirs(w)
			cs = m.refresh(now, nil)
		}
		if !cs.Empty() {
			if err := sink.Delta(ctx, cs); err != nil {
				return err
			}
		}
	}
}

// watchDirs watches each file's directory, so saves by rename are seen; a missing directory
// is retried at the next rescan.
func (m *Module) watchDirs(w *fsnotify.Watcher) {
	if w == nil {
		return
	}
	for _, s := range m.opts.Files {
		_ = w.Add(filepath.Dir(s.abs)) // retried at the next rescan
	}
}

// refresh rereads changed files and returns what changed since the last send. Files in force
// are reread even if their size and time look the same.
func (m *Module) refresh(now time.Time, force map[string]bool) *model.ChangeSet {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.reload(force)
	cs := m.tracker.Changes(m.world.ents, m.world.edges, now)
	cs.Events = m.newEvents(now)
	return cs
}

// reload rereads files that changed and rebuilds the world if any did.
func (m *Module) reload(force map[string]bool) {
	changed := false
	for i := range m.opts.Files {
		s, f := &m.opts.Files[i], &m.files[i]
		if st, _ := statFile(s.abs); f.read && st == f.stamp && !force[s.abs] {
			continue
		}
		*f = s.load(m.name)
		changed = true
	}
	if !changed {
		return
	}
	m.world = combine(m.opts.Files, m.files)
	m.health.Store(&data.Health{Err: m.world.err})
}

// newEvents turns problems not reported before into events.
func (m *Module) newEvents(now time.Time) []model.Event {
	var out []model.Event
	current := make(map[string]bool, len(m.world.reports))
	for _, r := range m.world.reports {
		current[r.msg] = true
		if m.reported[r.msg] {
			continue
		}
		m.seq++
		out = append(out, model.Event{
			ID: fmt.Sprintf("problem-%d", m.seq), At: now, Severity: r.sev,
			Kind: "problem", Message: r.msg, Source: m.name,
		})
	}
	m.reported = current
	m.events.Add(out...)
	return out
}

// Health reports files that are missing or unusable.
func (m *Module) Health() data.Health {
	if h := m.health.Load(); h != nil {
		return *h
	}
	return data.Health{}
}

// Discover returns the files' whole world without changing what Run has sent.
func (m *Module) Discover(ctx context.Context) (*model.ChangeSet, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.reload(nil)
	now := time.Now()
	cs := &model.ChangeSet{}
	for _, r := range sortedKeys(m.world.ents, cmp.Compare) {
		e := m.world.ents[r]
		e.Seen = now
		cs.Upserts = append(cs.Upserts, e)
	}
	for _, k := range sortedKeys(m.world.edges, module.CompareEdgeKeys) {
		cs.Edges = append(cs.Edges, m.world.edges[k])
	}
	return cs, nil
}

// Metrics lists the series the mapping reads.
func (m *Module) Metrics() []module.Metric { return slices.Clone(m.catalogue) }

// QuerySeries answers from the points last read, thinned to about one point per step.
func (m *Module) QuerySeries(ctx context.Context, q data.SeriesQuery) ([]data.Series, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []data.Series
	for _, e := range m.matching(q) {
		for _, name := range q.Metrics {
			ref := data.SeriesRef{Entity: e.Ref, Metric: name}
			ps, ok := m.world.points[ref]
			if !ok {
				continue
			}
			out = append(out, data.Series{Ref: ref, Unit: m.unit(name), Points: thin(within(ps, q.Window), q)})
		}
	}
	return out, nil
}

func (m *Module) matching(q data.SeriesQuery) []model.Entity {
	var out []model.Entity
	if len(q.Entities) > 0 {
		for _, r := range q.Entities {
			if e, ok := m.world.ents[r]; ok {
				out = append(out, e)
			}
		}
		return out
	}
	for _, r := range sortedKeys(m.world.ents, cmp.Compare) {
		if e := m.world.ents[r]; q.Filter.Match(&e) {
			out = append(out, e)
		}
	}
	return out
}

func (m *Module) unit(metric string) model.Unit {
	i := slices.IndexFunc(m.catalogue, func(c module.Metric) bool { return c.Name == metric })
	if i < 0 {
		return model.UnitNone
	}
	return m.catalogue[i].Unit
}

// within returns the points of sorted ps inside w, sharing ps's memory.
func within(ps []data.Point, w data.TimeWindow) []data.Point {
	from, _ := slices.BinarySearchFunc(ps, w.From.UnixNano(), func(p data.Point, t int64) int { return cmp.Compare(p.T, t) })
	to, _ := slices.BinarySearchFunc(ps, w.To.UnixNano(), func(p data.Point, t int64) int { return cmp.Compare(p.T, t) })
	return ps[from:to]
}

func thin(ps []data.Point, q data.SeriesQuery) []data.Point {
	if q.Step <= 0 {
		return slices.Clone(ps)
	}
	return data.Downsample(ps, q.Window, int(q.Window.Span()/q.Step))
}

// QueryEvents answers from the problem events sent so far.
func (m *Module) QueryEvents(ctx context.Context, q module.EventQuery) ([]model.Event, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.events.Query(q), nil
}

// Search finds entities whose name or id contains text, ignoring case.
func (m *Module) Search(ctx context.Context, text string, limit int) ([]model.EntityRef, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if limit <= 0 {
		return nil, errors.New("search limit must be positive")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	text = strings.ToLower(text)
	var out []model.EntityRef
	for _, r := range sortedKeys(m.world.ents, cmp.Compare) {
		e := m.world.ents[r]
		if len(out) < limit && (strings.Contains(strings.ToLower(e.Name), text) || strings.Contains(strings.ToLower(r.Native()), text)) {
			out = append(out, r)
		}
	}
	return out, nil
}
