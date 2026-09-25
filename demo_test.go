package file

import (
	"maps"
	"mindseye/internal/data"
	"mindseye/internal/demo"
	"mindseye/internal/kernel"
	"mindseye/internal/model"
	"mindseye/internal/module"
	"os"
	"slices"
	"testing"
	"time"
)

// demoModule configures the demo profile's file module, from the repository root.
func demoModule(tb testing.TB) *Module {
	tb.Helper()
	src, err := os.ReadFile("testdata/demo/config.yaml")
	if err != nil {
		tb.Fatal(err)
	}
	cfg, err := kernel.ParseConfig(src)
	if err != nil {
		tb.Fatal(err)
	}
	var reg module.Registry
	reg.Register(Kind, func() module.Module { return New() })
	var files []kernel.ModuleConfig
	for _, mc := range cfg.Modules {
		if mc.Kind == Kind {
			files = append(files, mc)
		}
	}
	insts, err := module.Build(tb.Context(), &reg, files)
	if err != nil || len(insts) != 1 {
		tb.Fatalf("built %d file modules: %v", len(insts), err)
	}
	return insts[0].Module.(*Module)
}

func TestDemoWorldLoads(t *testing.T) {
	t.Chdir("../..")
	m := demoModule(t)
	cs, err := m.Discover(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if h := m.Health(); h.Err != nil {
		t.Fatalf("health: %v", h.Err)
	}
	if n := len(cs.Upserts); n < 1800 || n > 2200 {
		t.Errorf("%d entities, want about 2000", n)
	}
	linked := map[model.EntityRef]bool{}
	for _, e := range cs.Edges {
		linked[e.From] = true
	}
	for _, e := range cs.Upserts {
		if !linked[e.Ref] && e.Kind != model.KindCluster && e.Kind != model.KindTeam {
			t.Errorf("%s links to nothing", e.Ref)
		}
	}
	if len(m.world.reports) != 0 {
		t.Errorf("problems loading the demo: %+v", m.world.reports)
	}
	host, _ := model.NewEntityRef("demo", model.KindHost, "ams1-db-001")
	series, err := m.QuerySeries(t.Context(), data.SeriesQuery{
		Entities: []model.EntityRef{host}, Metrics: []string{"cpu.utilisation"},
		Window: data.TimeWindow{From: time.Date(2026, 8, 31, 0, 0, 0, 0, time.UTC), To: time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)},
	})
	if err != nil || len(series) != 1 || len(series[0].Points) != 96 {
		t.Errorf("a day of host CPU: %+v, %v; want 96 points", series, err)
	}
}

// TestDemoWorldMatchesFiles checks the in-memory demo world against the committed CSVs.
func TestDemoWorldMatchesFiles(t *testing.T) {
	t.Chdir("../..")
	read, err := demoModule(t).Discover(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	want := demo.World(1, 1)
	byRef := map[model.EntityRef]model.Entity{}
	for _, e := range want.Upserts {
		byRef[e.Ref] = e
	}
	if len(read.Upserts) != len(byRef) {
		t.Errorf("read %d entities, the demo world has %d", len(read.Upserts), len(byRef))
	}
	for _, got := range read.Upserts {
		if w, ok := byRef[got.Ref]; !ok || !sameEntity(got, w) {
			t.Errorf("%s: read %+v, want %+v", got.Ref, got, w)
		}
	}
	keys := map[model.EdgeKey]bool{}
	for _, e := range want.Edges {
		keys[e.Key()] = true
	}
	for _, e := range read.Edges {
		if !keys[e.Key()] {
			t.Errorf("read edge %v, not in the demo world", e.Key())
		}
	}
	if len(read.Edges) != len(keys) {
		t.Errorf("read %d edges, the demo world has %d", len(read.Edges), len(keys))
	}
}

// sameEntity compares everything but Seen; attributes by their text, as CSV cells hold them.
func sameEntity(a, b model.Entity) bool {
	return a.Ref == b.Ref && a.Kind == b.Kind && a.Name == b.Name && a.Status == b.Status &&
		a.Source == b.Source && slices.Equal(a.Tags, b.Tags) &&
		maps.EqualFunc(a.Attrs, b.Attrs, func(x, y model.Value) bool { return x.String() == y.String() })
}

// BenchmarkDemoLoad times reading and mapping every demo file into a fresh module.
func BenchmarkDemoLoad(b *testing.B) {
	b.Chdir("../..")
	b.ReportAllocs()
	for b.Loop() {
		if _, err := demoModule(b).Discover(b.Context()); err != nil {
			b.Fatal(err)
		}
	}
}
