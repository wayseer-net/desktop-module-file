package file

import (
	"mindseye/pkg/sdk"
	"strings"
	"testing"
	"time"
)

// recording is the cpu series and host events, replayed or not.
func recording(t *testing.T, replay bool, now time.Time) *Module {
	t.Helper()
	opts := cpuOptions + strings.TrimPrefix(eventsOptions, "\nfiles:\n")
	if replay {
		opts += "replay: true\n"
	}
	m := configure(t, opts)
	m.now = func() time.Time { return now }
	return m
}

// newest is the latest sample and event time m holds.
func newest(t *testing.T, m *Module) (sample, event time.Time) {
	t.Helper()
	sink := runOnce(t, m)
	q := sdk.SeriesQuery{Entities: []sdk.EntityRef{ref("host", "db-07")}, Metrics: []string{"cpu.utilisation"}, Window: sdk.TimeWindow{From: time.Unix(0, 0), To: time.Date(2100, 1, 1, 0, 0, 0, 0, time.UTC)}}
	ss, err := m.QuerySeries(t.Context(), q)
	if err != nil || len(ss) != 1 {
		t.Fatalf("query: %v, %d series", err, len(ss))
	}
	ps := ss[0].Points
	for _, e := range fileEvents(sink.Events()) {
		event = later(event, e.At)
	}
	return ps[len(ps)-1].Time(), event
}

func TestReplayMovesTheRecordingSoItsNewestTimeIsWhenFirstLoaded(t *testing.T) {
	now := time.Date(2030, 5, 6, 7, 8, 9, 0, time.UTC)
	sample, event := newest(t, recording(t, false, now))
	rs, re := newest(t, recording(t, true, now))
	shift := now.Sub(later(sample, event))
	if !later(rs, re).Equal(now) {
		t.Errorf("replayed newest time %v, want %v", later(rs, re), now)
	}
	if rs.Sub(sample) != shift || re.Sub(event) != shift {
		t.Errorf("samples moved %v and events %v; want both moved %v", rs.Sub(sample), re.Sub(event), shift)
	}
}

func TestReplayKeepsItsShiftAcrossReloads(t *testing.T) {
	now := time.Date(2030, 5, 6, 7, 8, 9, 0, time.UTC)
	m := recording(t, true, now)
	first, _ := newest(t, m)
	m.now = func() time.Time { return now.Add(time.Hour) }
	m.mu.Lock()
	m.reload(map[string]bool{m.opts.Files[0].abs: true})
	m.mu.Unlock()
	if again, _ := newest(t, m); !again.Equal(first) {
		t.Errorf("after a reload the newest sample is at %v, want it still at %v", again, first)
	}
}

func later(a, b time.Time) time.Time {
	if a.After(b) {
		return a
	}
	return b
}
