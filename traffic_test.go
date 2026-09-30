package file

import (
	"mindseye/pkg/sdk"
	"os"
	"path/filepath"
	"testing"
)

// callsOptions maps calls.csv in dir: each service talks to the services in calls, at rates.
func callsOptions(t *testing.T, csv string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "calls.csv")
	if err := os.WriteFile(path, []byte(csv), 0o600); err != nil {
		t.Fatal(err)
	}
	return `
files:
  - path: ` + path + `
    entities:
      kind: service
      id: id
      edges:
        - {rel: talks_to, to: calls, kind: service, rate: rates, unit: requests}
`
}

func traffics(t *testing.T, m *Module) map[string]sdk.Traffic {
	t.Helper()
	cs, err := m.Discover(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]sdk.Traffic{}
	for _, e := range cs.Edges {
		out[e.From.Native()+">"+e.To.Native()] = e.Traffic
	}
	return out
}

func TestARateColumnGivesEachEdgeItsTraffic(t *testing.T) {
	m := configure(t, callsOptions(t, "id,calls,rates\nweb,api;auth,120;4.5\napi,db,\n"))
	got := traffics(t, m)
	want := map[string]sdk.Traffic{
		"web>api":  {Rate: 120, Unit: sdk.TrafficRequests},
		"web>auth": {Rate: 4.5, Unit: sdk.TrafficRequests},
		"api>db":   {},
	}
	if len(got) != len(want) {
		t.Fatalf("edges %v, want %v", got, want)
	}
	for k, w := range want {
		if got[k] != w {
			t.Errorf("%s carries %+v, want %+v", k, got[k], w)
		}
	}
}

func TestRatesThatDoNotFitTheirTargetsSkipTheRecord(t *testing.T) {
	m := configure(t, callsOptions(t, "id,calls,rates\n"+
		"a,b;c,1\n"+ // too few
		"d,e,-3\n"+ // negative
		"f,g,lots\n"+ // not a number
		"h,i,7\n"))
	if got := traffics(t, m); len(got) != 1 || got["h>i"] != (sdk.Traffic{Rate: 7, Unit: sdk.TrafficRequests}) {
		t.Errorf("edges %v; want only h>i at 7 requests/s", got)
	}
	wantProblems(t, runOnce(t, m).Events(), "calls.csv:2: ", "calls.csv:3: ", "calls.csv:4: ")
}

func TestARateNeedsAUnitAndAUnitARate(t *testing.T) {
	for _, edge := range []string{
		"{rel: talks_to, to: b, kind: host, rate: r}",
		"{rel: talks_to, to: b, kind: host, unit: requests}",
		"{rel: talks_to, to: b, kind: host, rate: r, unit: furlongs}",
	} {
		opts := "files: [{path: x.csv, entities: {kind: host, id: a, edges: [" + edge + "]}}]"
		if err := New().Configure(t.Context(), config(t, opts)); err == nil {
			t.Errorf("accepted %s", edge)
		}
	}
}
