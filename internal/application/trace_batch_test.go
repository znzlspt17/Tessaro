package application

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func callTraceBatch(t *testing.T, m *Manager, id string, cursor TraceCursor, limit int) ([]Envelope, TraceCursor, bool) {
	t.Helper()
	events, next, more, err := m.ReadTraceBatch(id, cursor, limit)
	if err != nil {
		t.Fatalf("reading trace: %v", err)
	}
	return events, next, more
}

func TestTraceBatchBoundsMemoryForLargeEvents(t *testing.T) {
	dir := t.TempDir()
	id := "2026-11-08.003"
	row := `{"kind":"search","data":{"text":"` + strings.Repeat("x", 3<<20) + `"}}` + "\n"
	if err := os.WriteFile(filepath.Join(dir, id+".trace.jsonl"), []byte(strings.Repeat(row, 4)), 0600); err != nil {
		t.Fatal(err)
	}
	m := New(Config{OutputDir: dir})
	a, next, more := callTraceBatch(t, m, id, TraceCursor{}, 100)
	if len(a) != 3 || !more {
		t.Fatalf("large batch should stop after its byte budget: events=%d more=%v", len(a), more)
	}
	b, _, more := callTraceBatch(t, m, id, next, 100)
	if len(b) != 1 || more {
		t.Fatalf("large events after byte boundary: events=%d more=%v", len(b), more)
	}
}

func TestTraceBatchPreservesEventsAcrossBoundariesAndFinalization(t *testing.T) {
	dir := t.TempDir()
	id := "2026-11-08.001"
	tmp := filepath.Join(dir, id+".trace.tmp")
	initial := "{\"seq\":4,\"kind\":\"search\",\"data\":{\"id\":\"a\"}}\n" +
		"{\"seq\":4,\"kind\":\"problem_skipped\",\"data\":{\"id\":\"b\"}}\n" +
		"{\"seq\":5,\"kind\":\"run_finished\",\"data\":{\"status\":\"cancelled\"}}\n" +
		"{\"seq\":6,\"kind\":\"search\""
	if err := os.WriteFile(tmp, []byte(initial), 0600); err != nil {
		t.Fatal(err)
	}
	m := New(Config{OutputDir: dir})
	a, cursor, more := callTraceBatch(t, m, id, TraceCursor{}, 2)
	if len(a) != 2 || a[0].Seq != 4 || a[1].Seq != 4 || a[1].Kind != "problem_skipped" || !more {
		t.Fatalf("first batch lost duplicate sequence or skipped result: %+v more=%v", a, more)
	}
	b, cursor, more := callTraceBatch(t, m, id, cursor, 2)
	if len(b) != 1 || b[0].Kind != "run_finished" || more {
		t.Fatalf("second batch: %+v more=%v", b, more)
	}
	c, next, more := callTraceBatch(t, m, id, cursor, 2)
	if len(c) != 0 || more || cursor != next {
		t.Fatalf("partial line advanced cursor: %+v %v", c, more)
	}
	f, err := os.OpenFile(tmp, os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.WriteString(",\"data\":{}}\n"); err != nil {
		t.Fatal(err)
	}
	if err = f.Close(); err != nil {
		t.Fatal(err)
	}
	if err = os.Rename(tmp, filepath.Join(dir, id+".trace.jsonl")); err != nil {
		t.Fatal(err)
	}
	d, _, more := callTraceBatch(t, m, id, next, 2)
	if len(d) != 1 || d[0].Seq != 6 || more {
		t.Fatalf("finalized continuation: %+v more=%v", d, more)
	}
}

func TestTraceBatchPreservesLegacySequenceAcrossBatches(t *testing.T) {
	dir := t.TempDir()
	id := "2026-11-08.002"
	trace := "{\"lexeme\":\"a\"}\n\n{\"trial_index\":1,\"event\":{}}\n{\"lexeme\":\"b\"}\n"
	if err := os.WriteFile(filepath.Join(dir, id+".trace.jsonl"), []byte(trace), 0600); err != nil {
		t.Fatal(err)
	}
	m := New(Config{OutputDir: dir})
	a, cursor, more := callTraceBatch(t, m, id, TraceCursor{}, 1)
	if len(a) != 1 || a[0].Seq != 1 || a[0].RunID != id || !more {
		t.Fatalf("first legacy: %+v more=%v", a, more)
	}
	b, cursor, more := callTraceBatch(t, m, id, cursor, 1)
	if len(b) != 1 || b[0].Seq != 2 || b[0].Kind != "comparison" || !more {
		t.Fatalf("second legacy: %+v more=%v", b, more)
	}
	c, _, _ := callTraceBatch(t, m, id, cursor, 2)
	if len(c) != 1 || c[0].Seq != 3 {
		t.Fatalf("third legacy: %+v", c)
	}
}
