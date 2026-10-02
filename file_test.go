package file

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
	"wayseer/pkg/sdk"
	"wayseer/pkg/sdk/sdktest"

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
	working, failing := conformanceOptions(t)
	sdktest.Conform(t, sdktest.Case{
		New:     func() sdk.Module { return New() },
		Name:    "inventory",
		Options: working,
		Failing: failing,
	})
}

// conformanceOptions are the working and failing options shared with the external file program.
func conformanceOptions(t *testing.T) (working, failing string) {
	t.Helper()
	src, err := os.ReadFile(fixtures + "conformance.yaml")
	if err != nil {
		t.Fatal(err)
	}
	var doc struct{ Working, Failing yaml.Node }
	if err := yaml.Unmarshal(src, &doc); err != nil {
		t.Fatal(err)
	}
	w, _ := yaml.Marshal(&doc.Working)
	f, _ := yaml.Marshal(&doc.Failing)
	return string(w), string(f)
}

func TestCSVEntitiesMapColumns(t *testing.T) {
	w := discover(t, hostsOptions)
	web2 := w.entity(t, "host", "web-02")
	if web2.Name != "web-02" || web2.Status != (sdk.Status{Level: sdk.StatusCrit, Reason: "unreachable"}) {
		t.Errorf("web-02 = %+v; want its id as name and crit status", web2)
	}
	if !slices.Equal(web2.Tags, []string{"prod", "web"}) {
		t.Errorf("tags = %q", web2.Tags)
	}
	db := w.entity(t, "host", "db-07")
	want := map[string]sdk.Value{"os": sdk.String("linux"), "cores": sdk.Number(16), "ssd": sdk.Bool(true)}
	for k, v := range want {
		if !db.Attrs[k].Equal(v) {
			t.Errorf("db-07 %s = %v, want %v", k, db.Attrs[k], v)
		}
	}
	if db.Name != "Database 07" || db.Status.Level != sdk.StatusOK {
		t.Errorf("db-07 = %+v", db)
	}
	w.wantEdges(t,
		"host/db-07 member_of team/data", "host/web-01 member_of team/web", "host/web-02 member_of team/web",
		"host/web-01 depends_on host/db-07", "host/web-02 depends_on host/db-07", "host/web-02 depends_on host/cache-1")
}

func TestUnitsNameAnAttributesUnit(t *testing.T) {
	w := discover(t, strings.Replace(hostsOptions, "attrs: [os, cores, ssd]", "attrs: [os, cores, ssd]\n      units: {cores: count}", 1))
	db := w.entity(t, "host", "db-07")
	if v := db.Attrs["cores"]; !v.Equal(sdk.Number(16).In(sdk.UnitCount)) {
		t.Errorf("cores = %v in %q, want 16 counted", v, v.Unit())
	}
	if u := db.Attrs["os"].Unit(); u != sdk.UnitNone {
		t.Errorf("os has unit %q", u)
	}
}

func TestJSONEntitiesFollowRecordsPath(t *testing.T) {
	m := configure(t, servicesOptions)
	w := discoverWith(t, m)
	if got := w.natives(); !slices.Equal(got, []string{"billing", "checkout"}) {
		t.Fatalf("services = %v; bad records should be skipped", got)
	}
	co := w.entity(t, "service", "checkout")
	if !co.Attrs["meta.replicas"].Equal(sdk.Number(3)) || !co.Attrs["meta.owner"].Equal(sdk.String("web")) {
		t.Errorf("checkout attrs = %v", co.Attrs)
	}
	if b := w.entity(t, "service", "billing"); b.Status.Level != sdk.StatusDown || !slices.Equal(b.Tags, []string{"pci"}) {
		t.Errorf("billing = %+v", b)
	}
	w.wantEdges(t, "service/checkout runs_on host/web-01", "service/checkout runs_on host/web-02",
		"service/billing runs_on host/db-07")
	wantProblems(t, runOnce(t, m).Events(), "services.json:8: ", "services.json:9: ")
}

func TestSeriesFromTimestampedRows(t *testing.T) {
	m := configure(t, cpuOptions)
	w := discoverWith(t, m)
	if got := w.natives(); !slices.Equal(got, []string{"cache-1", "db-07", "web-01"}) {
		t.Fatalf("implied entities = %v", got)
	}
	start := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	q := sdk.SeriesQuery{
		Entities: []sdk.EntityRef{ref("host", "db-07"), ref("host", "cache-1")},
		Metrics:  []string{"cpu.utilisation", "memory.utilisation"},
		Window:   sdk.TimeWindow{From: start, To: start.Add(time.Hour)},
	}
	got, err := m.QuerySeries(t.Context(), q)
	if err != nil {
		t.Fatal(err)
	}
	want := map[sdk.SeriesRef][]float64{
		{Entity: ref("host", "db-07"), Metric: "cpu.utilisation"}:      {12.5, 13, 14},
		{Entity: ref("host", "db-07"), Metric: "memory.utilisation"}:   {50, 52},
		{Entity: ref("host", "cache-1"), Metric: "cpu.utilisation"}:    {3},
		{Entity: ref("host", "cache-1"), Metric: "memory.utilisation"}: {10},
	}
	if len(got) != len(want) {
		t.Fatalf("%d series, want %d: %+v", len(got), len(want), got)
	}
	for _, s := range got {
		if !slices.Equal(values(s.Points), want[s.Ref]) || s.Unit != sdk.UnitPercent {
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
	if got := natives(first.Sets()[0].Upserts); !slices.Equal(got, []string{"a", "d"}) {
		t.Errorf("entities = %v; want the good rows only", got)
	}
	wantProblems(t, first.Events(), "bad.csv:3: ", "bad.csv:4: ", "bad.csv:5: ")
	if again := runOnce(t, m); len(again.Events()) != 0 {
		t.Errorf("a restart reported the problems again: %v", again.Events())
	}
}

func TestEditSendsOnlyTheChange(t *testing.T) {
	path := copyFixture(t, "hosts.csv")
	m := configure(t, strings.ReplaceAll(hostsOptions, fixtures+"hosts.csv", path))
	sink := runInBackground(t, m)
	sink.WaitFor(t, 1)
	rewrite(t, path, "web-01,Web 01,warn,disk 91% full", "web-01,Web 01,ok,")
	rewrite(t, path, "\nweb-02,,crit,unreachable,linux,8,false,prod; web,web,db-07;cache-1\n",
		"\nweb-03,,ok,,linux,4,false,,web,\n")
	cs := merged(t, sink, func(cs sdk.ChangeSet) bool { return len(cs.Removes) > 0 && hasUpsert(cs, "web-01") })
	if got := natives(cs.Upserts); !slices.Equal(got, []string{"web-01", "web-03"}) {
		t.Errorf("upserts = %v", got)
	}
	if !slices.Equal(cs.Removes, []sdk.EntityRef{ref("host", "web-02")}) {
		t.Errorf("removes = %v", cs.Removes)
	}
}

func TestDeleteRemovesEntitiesAndSurfacesError(t *testing.T) {
	path := copyFixture(t, "hosts.csv")
	m := configure(t, strings.ReplaceAll(hostsOptions, fixtures+"hosts.csv", path))
	sink := runInBackground(t, m)
	sink.WaitFor(t, 1)
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	cs := merged(t, sink, func(cs sdk.ChangeSet) bool { return len(cs.Removes) == 3 })
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
		"files: [{path: x.csv, series: {kind: host, id: a, time: t, metrics: {cpu: {field: c, unit: pct}}}}]",
		"files: [{path: x.csv, entities: {kind: host, id: a}}, {path: x.csv, entities: {kind: host, id: a}}]",
		"files: [{path: x.csv, entities: {kind: host, id: a}}]\nrescan: 1ms",
		"files: [{path: x.csv, entities: {kind: host, id: a, attrs: [m], units: {m: furlongs}}}]",
		"files: [{path: x.csv, entities: {kind: host, id: a, attrs: [m], units: {n: bytes}}}]", // n is no attribute
	} {
		if err := New().Configure(t.Context(), config(t, opts)); err == nil {
			t.Errorf("accepted %q", opts)
		}
	}
}

// helpers

func ref(kind sdk.Kind, native string) sdk.EntityRef {
	r, err := sdk.NewEntityRef("inventory", kind, native)
	if err != nil {
		panic(err)
	}
	return r
}

func config(t *testing.T, options string) sdk.Config {
	t.Helper()
	var doc yaml.Node
	if err := yaml.Unmarshal([]byte(options), &doc); err != nil {
		t.Fatal(err)
	}
	c := sdk.Config{Name: "inventory", Line: 1}
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
	ents  map[sdk.EntityRef]sdk.Entity
	edges []string
}

func discover(t *testing.T, options string) world { return discoverWith(t, configure(t, options)) }

func discoverWith(t *testing.T, m *Module) world {
	t.Helper()
	cs, err := m.Discover(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	w := world{ents: map[sdk.EntityRef]sdk.Entity{}}
	for _, e := range cs.Upserts {
		w.ents[e.Ref] = e
	}
	for _, e := range cs.Edges {
		w.edges = append(w.edges, strings.TrimPrefix(string(e.From), "inventory/")+" "+string(e.Rel)+" "+
			strings.TrimPrefix(string(e.To), "inventory/"))
	}
	return w
}

func (w world) entity(t *testing.T, kind sdk.Kind, native string) sdk.Entity {
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

func natives(ents []sdk.Entity) []string {
	var out []string
	for _, e := range ents {
		out = append(out, e.Ref.Native())
	}
	slices.Sort(out)
	return out
}

func hasUpsert(cs sdk.ChangeSet, native string) bool {
	return slices.Contains(natives(cs.Upserts), native)
}

func values(ps []sdk.Point) []float64 {
	var out []float64
	for _, p := range ps {
		out = append(out, p.V)
	}
	return out
}

// wantProblems requires one warning event per prefix, in order, from the module's source.
func wantProblems(t *testing.T, evs []sdk.Event, prefixes ...string) {
	t.Helper()
	if len(evs) != len(prefixes) {
		t.Fatalf("%d problem events, want %d: %+v", len(evs), len(prefixes), evs)
	}
	for i, e := range evs {
		if !strings.Contains(e.Message, prefixes[i]) || e.Severity != sdk.SevWarn || e.Source != "inventory" {
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

// merged waits until the deltas after the snapshot, merged, satisfy done, and returns them.
func merged(t *testing.T, s *sdktest.Sink, done func(sdk.ChangeSet) bool) sdk.ChangeSet {
	t.Helper()
	var cs sdk.ChangeSet
	sdktest.Eventually(t, func() bool {
		cs = sdk.ChangeSet{}
		for _, d := range s.Sets()[1:] {
			cs.Removes = append(cs.Removes, d.Removes...)
			cs.Upserts = append(cs.Upserts, d.Upserts...)
		}
		return done(cs)
	})
	return cs
}

// runOnce runs m until its snapshot arrives, then stops it.
func runOnce(t *testing.T, m *Module) *sdktest.Sink {
	t.Helper()
	ctx, cancel := context.WithCancel(t.Context())
	sink, done := &sdktest.Sink{}, make(chan error, 1)
	go func() { done <- m.Run(ctx, sink) }()
	sink.WaitFor(t, 1)
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	return sink
}

func runInBackground(t *testing.T, m *Module) *sdktest.Sink {
	t.Helper()
	return sdktest.Run(t, func(ctx context.Context, s *sdktest.Sink) error { return m.Run(ctx, s) })
}

func TestUnchangedFilesAreStillSentEachRescan(t *testing.T) {
	m := configure(t, hostsOptions+"rescan: 100ms\n")
	sink := runInBackground(t, m)
	sink.WaitFor(t, 3)
	for i, cs := range sink.Sets()[1:] {
		if !cs.Empty() {
			t.Errorf("delta %d of unchanged files = %+v, want empty", i, cs)
		}
	}
}
