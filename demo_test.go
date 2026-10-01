package file

import (
	"os"
	"slices"
	"testing"
	"time"
	"wayseer/pkg/sdk"

	"go.yaml.in/yaml/v3"
)

// demoEntry is a module entry in the demo profile's config.
type demoEntry struct {
	Kind    string       `yaml:"kind"`
	Name    sdk.ModuleID `yaml:"name"`
	Options yaml.Node    `yaml:"options"`
}

// demoModule configures the demo profile's file module, from the repository root, with the
// recording's own times.
func demoModule(tb testing.TB) *Module {
	tb.Helper()
	src, err := os.ReadFile("testdata/demo/config.yaml")
	if err != nil {
		tb.Fatal(err)
	}
	var cfg struct {
		Modules []demoEntry `yaml:"modules"`
	}
	if err := yaml.Unmarshal(src, &cfg); err != nil {
		tb.Fatal(err)
	}
	i := slices.IndexFunc(cfg.Modules, func(mc demoEntry) bool { return mc.Kind == Kind })
	if i < 0 {
		tb.Fatal("the demo profile has no file module")
	}
	m := New()
	if err := m.Configure(tb.Context(), sdk.Config{Name: cfg.Modules[i].Name, Options: cfg.Modules[i].Options}); err != nil {
		tb.Fatal(err)
	}
	if !m.opts.Replay {
		tb.Error("the demo profile should replay its recording up to now")
	}
	m.opts.Replay = false
	return m
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
	linked := map[sdk.EntityRef]bool{}
	for _, e := range cs.Edges {
		linked[e.From] = true
	}
	for _, e := range cs.Upserts {
		if !linked[e.Ref] && e.Kind != sdk.KindCluster && e.Kind != sdk.KindTeam {
			t.Errorf("%s links to nothing", e.Ref)
		}
	}
	if n := len(m.world.events); n < 20 {
		t.Errorf("%d events, want at least 20", n)
	}
	if len(m.world.reports) != 0 {
		t.Errorf("problems loading the demo: %+v", m.world.reports)
	}
	host, _ := sdk.NewEntityRef("demo", sdk.KindHost, "ams1-db-001")
	series, err := m.QuerySeries(t.Context(), sdk.SeriesQuery{
		Entities: []sdk.EntityRef{host}, Metrics: []string{"cpu.utilisation"},
		Window: sdk.TimeWindow{From: time.Date(2026, 8, 31, 0, 0, 0, 0, time.UTC), To: time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)},
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

// BenchmarkQuerySeriesDay times a day of one host's CPU, as a sparkline asks for it.
func BenchmarkQuerySeriesDay(b *testing.B) {
	b.Chdir("../..")
	m := demoModule(b)
	if _, err := m.Discover(b.Context()); err != nil {
		b.Fatal(err)
	}
	host, _ := sdk.NewEntityRef("demo", sdk.KindHost, "ams1-db-001")
	q := sdk.SeriesQuery{
		Entities: []sdk.EntityRef{host}, Metrics: []string{"cpu.utilisation"}, Step: 30 * time.Minute,
		Window: sdk.TimeWindow{From: time.Date(2026, 8, 31, 0, 0, 0, 0, time.UTC), To: time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)},
	}
	b.ReportAllocs()
	for b.Loop() {
		if ss, err := m.QuerySeries(b.Context(), q); err != nil || len(ss) != 1 {
			b.Fatalf("%d series, %v", len(ss), err)
		}
	}
}
