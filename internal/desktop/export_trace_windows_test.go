//go:build windows

package desktop

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"tessaro/internal/application"
)

func TestExportTraceStreamsEquivalentEventsAndPreservesTargetOnError(t *testing.T) {
	dir := t.TempDir()
	id := "2026-11-08.001"
	var source bytes.Buffer
	for n := 1; n <= 1200; n++ {
		fmt.Fprintf(&source, "{\"seq\":%d,\"run_id\":%q,\"kind\":\"search\",\"data\":{\"step\":%d}}\n", n, id, n)
	}
	if err := os.WriteFile(filepath.Join(dir, id+".trace.jsonl"), source.Bytes(), 0600); err != nil {
		t.Fatal(err)
	}
	m := application.New(application.Config{OutputDir: dir})
	path := filepath.Join(dir, "export.jsonl")
	if err := os.WriteFile(path, []byte("old"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := exportTraceFile(context.Background(), m, id, path); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	events, err := m.ReadTrace(id)
	if err != nil {
		t.Fatal(err)
	}
	var expected bytes.Buffer
	for _, event := range events {
		b, err := json.Marshal(event)
		if err != nil {
			t.Fatal(err)
		}
		expected.Write(b)
		expected.WriteByte('\n')
	}
	if !bytes.Equal(got, expected.Bytes()) {
		t.Fatal("export differs from original JSONL behavior")
	}
	if err := exportTraceFile(context.Background(), m, "2026-11-08.999", path); err == nil {
		t.Fatal("missing trace should fail")
	}
	again, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(again, got) {
		t.Fatal("failed export replaced existing destination")
	}
}
