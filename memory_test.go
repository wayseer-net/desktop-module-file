package file

import (
	"runtime"
	"slices"
	"testing"
	"time"
)

// The file module holds a world in memory for as long as it runs; these tests keep what it
// holds per sample and per entity from growing unseen (§11: 250k entities under 1.5 GB).

// An evenly spaced sample is its 8-byte value, as the demo's mostly are; its series and entity
// add a little, but replay and slack may not multiply it.
func TestSamplesCostLittleMoreThanTheirBytes(t *testing.T) {
	m, held := loadDemoParts(t, func(s source) bool { return s.Series != nil })
	n := 0
	for _, s := range m.world.points {
		n += s.len()
	}
	if n < 50000 {
		t.Fatalf("only %d samples loaded", n)
	}
	per := float64(held) / float64(n)
	t.Logf("%.1f bytes per sample", per)
	if per > 14 {
		t.Errorf("%.1f bytes held per sample, want at most 14", per)
	}
}

func TestEntitiesCostWhatTheyCarry(t *testing.T) {
	m, held := loadDemoParts(t, func(s source) bool { return s.Entities != nil })
	n := len(m.world.ents)
	if n < 1500 {
		t.Fatalf("only %d entities loaded", n)
	}
	per := float64(held) / float64(n)
	t.Logf("%.0f bytes per entity", per)
	if per > 1100 {
		t.Errorf("%.0f bytes held per entity with its edges, want at most 1100", per)
	}
}

// loadDemoParts runs the file module over the committed demo files keep picks, with replay,
// and returns the heap it holds after a GC.
func loadDemoParts(t *testing.T, keep func(source) bool) (*Module, uint64) {
	t.Helper()
	t.Chdir("../..")
	m := demoModule(t)
	m.opts.Replay = true
	m.opts.Files = slices.DeleteFunc(m.opts.Files, func(s source) bool { return !keep(s) })
	m.files = m.files[:len(m.opts.Files)]
	before := heapInUse()
	cs := m.refresh(time.Now(), nil)
	if m.Health().Err != nil || cs.Empty() {
		t.Fatalf("nothing loaded: %v", m.Health().Err)
	}
	cs = nil
	held := heapInUse() - before
	runtime.KeepAlive(cs)
	return m, held
}

func heapInUse() uint64 {
	runtime.GC()
	var ms runtime.MemStats
	runtime.ReadMemStats(&ms)
	return ms.HeapAlloc
}
