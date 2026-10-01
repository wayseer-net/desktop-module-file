package file

import (
	"slices"
	"strings"
	"testing"
	"time"
	"wayseer/pkg/sdk"
)

const eventsOptions = `
files:
  - path: ` + fixtures + `events.csv
    events:
      kind: host
      id: host
      time: ts
      severity: level
      type: what
      message: text
      fields: [version]
`

func TestCSVEventsMapColumns(t *testing.T) {
	evs := fileEvents(runOnce(t, configure(t, eventsOptions)).Events())
	at := time.Date(2026, 9, 1, 10, 0, 0, 0, time.UTC)
	want := []sdk.Event{
		{
			Entity: ref("host", "web-01"), At: at, Severity: sdk.SevInfo, Kind: "deploy", Message: "deployed 1.4",
			Fields: map[string]sdk.Value{"version": sdk.Number(1.4)},
		},
		{Entity: ref("host", "web-02"), At: at.Add(5 * time.Minute), Severity: sdk.SevCritical, Kind: "alert", Message: "unreachable"},
		{At: at.Add(10 * time.Minute), Severity: sdk.SevWarn, Kind: "event", Message: "maintenance window opens"},
	}
	if len(evs) != len(want) {
		t.Fatalf("%d events, want %d (the repeated row once): %+v", len(evs), len(want), evs)
	}
	for i, w := range want {
		w.Source = "inventory"
		if !sameEvent(evs[i], w) {
			t.Errorf("event %d = %+v\nwant %+v", i, evs[i], w)
		}
	}
}

func TestBadEventRowsAreReported(t *testing.T) {
	sink := runOnce(t, configure(t, eventsOptions))
	var probs []sdk.Event
	for _, e := range sink.Events() {
		if e.Kind == "problem" {
			probs = append(probs, e)
		}
	}
	wantProblems(t, probs, "events.csv:6: ts", "events.csv:7: level", "events.csv:8: empty text")
}

func TestEventsAreSentOnce(t *testing.T) {
	m := configure(t, eventsOptions)
	first := runOnce(t, m)
	if len(fileEvents(first.Events())) == 0 {
		t.Fatal("no events in the snapshot")
	}
	if again := runOnce(t, m); len(again.Events()) != 0 {
		t.Errorf("a restart sent events again: %+v", again.Events())
	}
}

func TestEventIDsAreStableAcrossInstances(t *testing.T) {
	a := fileEvents(runOnce(t, configure(t, eventsOptions)).Events())
	b := fileEvents(runOnce(t, configure(t, eventsOptions)).Events())
	ids := func(evs []sdk.Event) []string {
		var out []string
		for _, e := range evs {
			out = append(out, e.ID)
		}
		return out
	}
	if got := ids(a); !slices.Equal(got, ids(b)) || slices.Contains(got, "") {
		t.Errorf("ids %v and %v; want the same, none empty", got, ids(b))
	}
}

func TestQueryEventsAnswersFromFileEvents(t *testing.T) {
	m := configure(t, eventsOptions)
	runOnce(t, m)
	from := time.Date(2026, 9, 1, 10, 1, 0, 0, time.UTC)
	got, err := m.QueryEvents(t.Context(), sdk.EventQuery{Window: sdk.TimeWindow{From: from, To: from.Add(time.Hour)}})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].Message != "unreachable" || got[1].Message != "maintenance window opens" {
		t.Errorf("events = %+v; want the two after 10:01, oldest first", got)
	}
}

func TestMissingEventColumnNamesFileAndColumn(t *testing.T) {
	m := configure(t, strings.Replace(eventsOptions, "severity: level", "severity: sev", 1))
	if _, err := m.Discover(t.Context()); err != nil {
		t.Fatal(err)
	}
	err := m.Health().Err
	if err == nil || !strings.Contains(err.Error(), "events.csv") || !strings.Contains(err.Error(), `"sev"`) {
		t.Errorf("health = %v; want the file and column named", err)
	}
}

func TestBadEventOptionsNameTheFile(t *testing.T) {
	for _, opts := range []string{
		"files: [{path: e.csv, events: {message: m}}]",                      // no time
		"files: [{path: e.csv, events: {time: t}}]",                         // no message
		"files: [{path: e.csv, events: {time: t, message: m, id: h}}]",      // id without kind
		"files: [{path: e.csv, events: {time: t, message: m, kind: host}}]", // kind without id
		"files: [{path: e.csv, events: {time: t, message: m, kind: Bad, id: h}}]",
	} {
		err := New().Configure(t.Context(), config(t, opts))
		if err == nil || !strings.Contains(err.Error(), "e.csv") {
			t.Errorf("%q: err = %v; want it rejected naming the file", opts, err)
		}
	}
}

// fileEvents drops the module's problem reports.
func fileEvents(evs []sdk.Event) []sdk.Event {
	return slices.DeleteFunc(slices.Clone(evs), func(e sdk.Event) bool { return e.Kind == "problem" })
}

// sameEvent compares everything but the ID.
func sameEvent(got, want sdk.Event) bool {
	return got.Entity == want.Entity && got.At.Equal(want.At) && got.Severity == want.Severity &&
		got.Kind == want.Kind && got.Message == want.Message && got.Source == want.Source &&
		attrsMatch(got.Fields, want.Fields)
}

func attrsMatch(a, b map[string]sdk.Value) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if !v.Equal(b[k]) {
			return false
		}
	}
	return true
}
