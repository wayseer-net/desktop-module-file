package file

import (
	"context"
	"mindseye/internal/data"
	"mindseye/internal/model"
	"mindseye/internal/module"
	"mindseye/internal/module/conformance"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"go.yaml.in/yaml/v3"
)

const fixtures = "../../testdata/file/"

const hostsOptions = `
files:
  - path: ` + fixtures + `hosts.csv
    entities:
      kind: host
      id: hostname
      name: display
      status: state
      reason: note
      attrs: [os, cores, ssd]
      tags: tags
      edges:
        - {rel: member_of, to: team, kind: team}
        - {rel: depends_on, to: depends, kind: host}
`

const servicesOptions = `
files:
  - path: ` + fixtures + `services.json
    records: data.services
    entities:
      kind: service
      id: id
      name: name
      status: health
      attrs: [meta.owner, meta.replicas]
      tags: tags
      edges:
        - {rel: runs_on, to: hosts, kind: host}
`

const cpuOptions = `
files:
  - path: ` + fixtures + `cpu.csv
    series:
      kind: host
      id: host
      time: ts
      metrics:
        cpu.utilisation: {field: cpu, unit: percent}
        memory.utilisation: {field: mem, unit: percent}
`

func TestConformance(t *testing.T) {
	conformance.Run(t, conformance.Case{
		New:     func() module.Module { return New() },
		Name:    "inventory",
		Options: hostsOptions + strings.TrimPrefix(cpuOptions, "\nfiles:\n"),
		Failing: "files: [{path: /nonexistent/mindseye/hosts.csv, entities: {kind: host, id: hostname}}]",
	})
}

func TestCSVEntitiesMapColumns(t *testing.T) {
	w := discover(t, hostsOptions)
	web2 := w.entity(t, "host", "web-02")
	if web2.Name != "web-02" || web2.Status != (model.Status{Level: model.StatusCrit, Reason: "unreachable"}) {
		t.Errorf("web-02 = %+v; want its id as name and crit status", web2)
	}
	if !slices.Equal(web2.Tags, []string{"prod", "web"}) {
		t.Errorf("tags = %q", web2.Tags)
	}
	db := w.entity(t, "host", "db-07")
	want := map[string]model.Value{"os": model.String("linux"), "cores": model.Number(16), "ssd": model.Bool(true)}
	for k, v := range want {
		if !db.Attrs[k].Equal(v) {
			t.Errorf("db-07 %s = %v, want %v", k, db.Attrs[k], v)
		}
	}
	if db.Name != "Database 07" || db.Status.Level != model.StatusOK {
		t.Errorf("db-07 = %+v", db)
	}
	w.wantEdges(t,
		"host/db-07 member_of team/data", "host/web-01 member_of team/web", "host/web-02 member_of team/web",
		"host/web-01 depends_on host/db-07", "host/web-02 depends_on host/db-07", "host/web-02 depends_on host/cache-1")
}

func TestJSONEntitiesFollowRecordsPath(t *testing.T) {
	m := configure(t, servicesOptions)
	w := discoverWith(t, m)
	if got := w.natives(); !slices.Equal(got, []string{"billing", "checkout"}) {
		t.Fatalf("services = %v; bad records should be skipped", got)
	}
	co := w.entity(t, "service", "checkout")
	if !co.Attrs["meta.replicas"].Equal(model.Number(3)) || !co.Attrs["meta.owner"].Equal(model.String("web")) {
		t.Errorf("checkout attrs = %v", co.Attrs)
	}
	if b := w.entity(t, "service", "billing"); b.Status.Level != model.StatusDown || !slices.Equal(b.Tags, []string{"pci"}) {
		t.Errorf("billing = %+v", b)
	}
	w.wantEdges(t, "service/checkout runs_on host/web-01", "service/checkout runs_on host/web-02",
		"service/billing runs_on host/db-07")
	wantProblems(t, runOnce(t, m).events(), "services.json:8: ", "services.json:9: ")
}

func TestSeriesFromTimestampedRows(t *testing.T) {
	m := configure(t, cpuOptions)
	w := discoverWith(t, m)
	if got := w.natives(); !slices.Equal(got, []string{"cache-1", "db-07", "web-01"}) {
		t.Fatalf("implied entities = %v", got)
	}
	start := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	q := data.SeriesQuery{
		Entities: []model.EntityRef{ref("host", "db-07"), ref("host", "cache-1")},
		Metrics:  []string{"cpu.utilisation", "memory.utilisation"},
		Window:   data.TimeWindow{From: start, To: start.Add(time.Hour)},
	}
	got, err := m.QuerySeries(t.Context(), q)
	if err != nil {
		t.Fatal(err)
	}
	want := map[data.SeriesRef][]float64{
		{Entity: ref("host", "db-07"), Metric: "cpu.utilisation"}:      {12.5, 13, 14},
		{Entity: ref("host", "db-07"), Metric: "memory.utilisation"}:   {50, 52},
		{Entity: ref("host", "cache-1"), Metric: "cpu.utilisation"}:    {3},
		{Entity: ref("host", "cache-1"), Metric: "memory.utilisation"}: {10},
	}
	if len(got) != len(want) {
		t.Fatalf("%d series, want %d: %+v", len(got), len(want), got)
	}
	for _, s := range got {
		if !slices.Equal(values(s.Points), want[s.Ref]) || s.Unit != model.UnitPercent {
			t.Errorf("%v = %v %s, want %v", s.Ref, values(s.Points), s.Unit, want[s.Ref])
		}
	}
	if c := got[0].Points; len(c) == 3 && c[1].Time() != start.Add(30*time.Second) {
		t.Errorf("points out of time order: %v", c)
	}
}

func TestBadRowsAreReportedOnceAndSkipped(t *testing.T) {
	m := configure(t, `
files:
  - path: `+fixtures+`bad.csv
    entities: {kind: host, id: hostname, status: state, attrs: [cores]}
`)
	first := runOnce(t, m)
	if got := natives(first.sets[0].Upserts); !slices.Equal(got, []string{"a", "d"}) {
		t.Errorf("entities = %v; want the good rows only", got)
	}
	wantProblems(t, first.events(), "bad.csv:3: ", "bad.csv:4: ", "bad.csv:5: ")
	if again := runOnce(t, m); len(again.events()) != 0 {
		t.Errorf("a restart reported the problems again: %v", again.events())
	}
}

func TestEditSendsOnlyTheChange(t *testing.T) {
	path := copyFixture(t, "hosts.csv")
	m := configure(t, strings.ReplaceAll(hostsOptions, fixtures+"hosts.csv", path))
	sink := runInBackground(t, m)
	sink.waitFor(t, 1)
	rewrite(t, path, "web-01,Web 01,warn,disk 91% full", "web-01,Web 01,ok,")
	rewrite(t, path, "\nweb-02,,crit,unreachable,linux,8,false,prod; web,web,db-07;cache-1\n",
		"\nweb-03,,ok,,linux,4,false,,web,\n")
	cs := sink.merged(t, func(cs model.ChangeSet) bool { return len(cs.Removes) > 0 && hasUpsert(cs, "web-01") })
	if got := natives(cs.Upserts); !slices.Equal(got, []string{"web-01", "web-03"}) {
		t.Errorf("upserts = %v", got)
	}
	if !slices.Equal(cs.Removes, []model.EntityRef{ref("host", "web-02")}) {
		t.Errorf("removes = %v", cs.Removes)
	}
}

func TestDeleteRemovesEntitiesAndSurfacesError(t *testing.T) {
	path := copyFixture(t, "hosts.csv")
	m := configure(t, strings.ReplaceAll(hostsOptions, fixtures+"hosts.csv", path))
	sink := runInBackground(t, m)
	sink.waitFor(t, 1)
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	cs := sink.merged(t, func(cs model.ChangeSet) bool { return len(cs.Removes) == 3 })
	if len(cs.Upserts) != 0 {
		t.Errorf("upserts after delete: %v", natives(cs.Upserts))
	}
	if err := m.Health().Err; err == nil || !strings.Contains(err.Error(), "hosts.csv") {
		t.Errorf("health = %v; want the missing file named", err)
	}
}

func TestBadOptionsRejected(t *testing.T) {
	for _, opts := range []string{
		"files: []",
		"files: [{path: x.csv}]", // nothing mapped
		"files: [{path: x.csv, entities: {id: a}}]",                           // no kind
		"files: [{path: x.csv, entities: {kind: host}}]",                      // no id
		"files: [{path: x.txt, entities: {kind: host, id: a}}]",               // unknown format
		"files: [{path: x.csv, records: a.b, entities: {kind: host, id: a}}]", // records is JSON only
		"files: [{path: x.csv, delimiter: ab, entities: {kind: host, id: a}}]",
		"files: [{path: x.csv, entities: {kind: host, id: a, edges: [{rel: Bad, to: b, kind: host}]}}]",
		"files: [{path: x.csv, series: {kind: host, id: a, time: t}}]", // no metrics
		"files: [{path: x.csv, entities: {kind: host, id: a}}, {path: x.csv, entities: {kind: host, id: a}}]",
		"files: [{path: x.csv, entities: {kind: host, id: a}}]\nrescan: 1ms",
	} {
		if err := New().Configure(t.Context(), config(t, opts)); err == nil {
			t.Errorf("accepted %q", opts)
		}
	}
}

// helpers

func ref(kind model.Kind, native string) model.EntityRef {
	r, err := model.NewEntityRef("inventory", kind, native)
	if err != nil {
		panic(err)
	}
	return r
}

func config(t *testing.T, options string) module.Config {
	t.Helper()
	var doc yaml.Node
	if err := yaml.Unmarshal([]byte(options), &doc); err != nil {
		t.Fatal(err)
	}
	c := module.Config{Name: "inventory", Line: 1}
	if len(doc.Content) > 0 {
		c.Options = *doc.Content[0]
	}
	return c
}

func configure(t *testing.T, options string) *Module {
	t.Helper()
	m := New()
	if err := m.Configure(t.Context(), config(t, options)); err != nil {
		t.Fatal(err)
	}
	return m
}

type world struct {
	ents  map[model.EntityRef]model.Entity
	edges []string
}

func discover(t *testing.T, options string) world { return discoverWith(t, configure(t, options)) }

func discoverWith(t *testing.T, m *Module) world {
	t.Helper()
	cs, err := m.Discover(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	w := world{ents: map[model.EntityRef]model.Entity{}}
	for _, e := range cs.Upserts {
		w.ents[e.Ref] = e
	}
	for _, e := range cs.Edges {
		w.edges = append(w.edges, strings.TrimPrefix(string(e.From), "inventory/")+" "+string(e.Rel)+" "+
			strings.TrimPrefix(string(e.To), "inventory/"))
	}
	return w
}

func (w world) entity(t *testing.T, kind model.Kind, native string) model.Entity {
	t.Helper()
	e, ok := w.ents[ref(kind, native)]
	if !ok {
		t.Fatalf("no %s %s among %v", kind, native, w.natives())
	}
	return e
}

func (w world) natives() []string {
	var out []string
	for r := range w.ents {
		out = append(out, r.Native())
	}
	slices.Sort(out)
	return out
}

func (w world) wantEdges(t *testing.T, want ...string) {
	t.Helper()
	got := slices.Sorted(slices.Values(w.edges))
	slices.Sort(want)
	if !slices.Equal(got, want) {
		t.Errorf("edges =\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

func natives(ents []model.Entity) []string {
	var out []string
	for _, e := range ents {
		out = append(out, e.Ref.Native())
	}
	slices.Sort(out)
	return out
}

func hasUpsert(cs model.ChangeSet, native string) bool {
	return slices.Contains(natives(cs.Upserts), native)
}

func values(ps []data.Point) []float64 {
	var out []float64
	for _, p := range ps {
		out = append(out, p.V)
	}
	return out
}

// wantProblems requires one warning event per prefix, in order, from the module's source.
func wantProblems(t *testing.T, evs []model.Event, prefixes ...string) {
	t.Helper()
	if len(evs) != len(prefixes) {
		t.Fatalf("%d problem events, want %d: %+v", len(evs), len(prefixes), evs)
	}
	for i, e := range evs {
		if !strings.Contains(e.Message, prefixes[i]) || e.Severity != model.SevWarn || e.Source != "inventory" {
			t.Errorf("event %d = %+v, want a warning containing %q", i, e, prefixes[i])
		}
	}
}

func copyFixture(t *testing.T, name string) string {
	t.Helper()
	src, err := os.ReadFile(fixtures + name)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, src, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// rewrite replaces old with replacement in the file, the way an editor saves: write aside, then rename.
func rewrite(t *testing.T, path, old, replacement string) {
	t.Helper()
	src, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(src), old) {
		t.Fatalf("%s does not contain %q", path, old)
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, []byte(strings.Replace(string(src), old, replacement, 1)), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(tmp, path); err != nil {
		t.Fatal(err)
	}
}

// recSink records what a module sends.
type recSink struct {
	mu   sync.Mutex
	sets []model.ChangeSet
}

func (s *recSink) Snapshot(_ context.Context, cs *model.ChangeSet) error { return s.add(cs) }
func (s *recSink) Delta(_ context.Context, cs *model.ChangeSet) error    { return s.add(cs) }

func (s *recSink) add(cs *model.ChangeSet) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sets = append(s.sets, *cs)
	return nil
}

func (s *recSink) events() []model.Event {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []model.Event
	for _, cs := range s.sets {
		out = append(out, cs.Events...)
	}
	return out
}

func (s *recSink) waitFor(t *testing.T, n int) {
	t.Helper()
	eventually(t, func() bool { s.mu.Lock(); defer s.mu.Unlock(); return len(s.sets) >= n })
}

// merged waits until the deltas after the snapshot, merged, satisfy done, and returns them.
func (s *recSink) merged(t *testing.T, done func(model.ChangeSet) bool) model.ChangeSet {
	t.Helper()
	var cs model.ChangeSet
	eventually(t, func() bool {
		s.mu.Lock()
		defer s.mu.Unlock()
		cs = model.ChangeSet{}
		for _, d := range s.sets[1:] {
			cs.Removes = append(cs.Removes, d.Removes...)
			cs.Upserts = append(cs.Upserts, d.Upserts...)
		}
		return done(cs)
	})
	return cs
}

// runOnce runs m until its snapshot arrives, then stops it.
func runOnce(t *testing.T, m *Module) *recSink {
	t.Helper()
	ctx, cancel := context.WithCancel(t.Context())
	sink, done := &recSink{}, make(chan error, 1)
	go func() { done <- m.Run(ctx, sink) }()
	sink.waitFor(t, 1)
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	return sink
}

func runInBackground(t *testing.T, m *Module) *recSink {
	t.Helper()
	ctx, cancel := context.WithCancel(t.Context())
	sink, done := &recSink{}, make(chan error, 1)
	go func() { done <- m.Run(ctx, sink) }()
	t.Cleanup(func() { cancel(); <-done })
	return sink
}

// eventually polls cond until it holds or three seconds pass.
func eventually(t *testing.T, cond func() bool) {
	t.Helper()
	for deadline := time.Now().Add(3 * time.Second); !cond(); time.Sleep(10 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatal("condition not met in time")
		}
	}
}
