package file

import (
	"mindseye/internal/data"
	"mindseye/internal/kernel"
	"mindseye/internal/model"
	"mindseye/internal/module"
	"os"
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
