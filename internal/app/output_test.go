package app

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func TestWriteResultDateSequenceAndBytes(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2026, 11, 5, 16, 0, 0, 0, time.UTC)
	for i := 1; i <= 2; i++ {
		var out bytes.Buffer
		if err := WriteResult(&out, dir, now, map[string]int{"value": i}); err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(dir, fmt.Sprintf("2026-06-11.%03d", i))
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(data, out.Bytes()) {
			t.Fatal("file differs from stdout")
		}
	}
}
func TestWriteResultConcurrentAndNoOverwrite(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC)
	first := filepath.Join(dir, "2026-02-01.001")
	if err := os.WriteFile(first, []byte("existing"), 0600); err != nil {
		t.Fatal(err)
	}
	const n = 12
	errs := make(chan error, n)
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) { defer wg.Done(); errs <- WriteResult(&bytes.Buffer{}, dir, now, i) }(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	data, err := os.ReadFile(first)
	if err != nil || string(data) != "existing" {
		t.Fatalf("existing overwritten: %q %v", data, err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) != n+1 {
		t.Fatalf("entries %d, %v", len(entries), err)
	}
}
func TestWriteResultFailures(t *testing.T) {
	dir := t.TempDir()
	block := filepath.Join(dir, "file")
	if err := os.WriteFile(block, []byte("x"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := WriteResult(&bytes.Buffer{}, block, time.Now(), true); err == nil {
		t.Fatal("expected directory failure")
	}
	full := filepath.Join(dir, "full")
	if err := os.Mkdir(full, 0700); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC)
	for i := 1; i <= 999; i++ {
		if err := os.WriteFile(filepath.Join(full, fmt.Sprintf("2026-02-01.%03d", i)), nil, 0600); err != nil {
			t.Fatal(err)
		}
	}
	if err := WriteResult(&bytes.Buffer{}, full, now, true); err == nil {
		t.Fatal("expected exhausted sequence failure")
	}
}
