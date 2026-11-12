package application

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"tessaro/internal/compare"
	"tessaro/internal/rank"
	"tessaro/internal/runner"
)

func fixtureResolver(context.Context, compare.Model) (rank.Ranker, error) { return rank.Fake{}, nil }

// 2026-11-07: 삭제하는 명령의 부분 보고서·heldout 지연 읽기 보장을 GUI 실행 계층으로 이전한다.
func TestComparisonPreservesPartialReportAndTrace(t *testing.T) {
	dir := t.TempDir()
	input := `{"intent":"identity","inputs":["x"],"allowed_types":["int"],"public_tests":[{"inputs":{"x":1},"want":1}],"budgets":{"max_candidates":1}}`
	manifest := `{"models":[{"id":"fixture","ranker":"jev"}],"problems":[{"id":"p","spec":"p.json","heldout":"missing.json"}],"strategies":["beam"]}`
	for name, data := range map[string]string{"p.json": input, "compare.json": manifest} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
	}
	m := New(Config{OutputDir: filepath.Join(dir, "output"), Resolve: fixtureResolver})
	id, err := m.Start(Request{Kind: "compare", ManifestPath: filepath.Join(dir, "compare.json")})
	if err != nil {
		t.Fatal(err)
	}
	s := waitEnd(t, m)
	var report compare.Report
	if err := json.Unmarshal(s.Payload, &report); err != nil {
		t.Fatal(err)
	}
	if report.Attempted != 1 || report.HeldoutHashes["p"] != "not_loaded" || len(report.Outcomes) != 1 || report.Outcomes[0].Result.Status != "budget_exhausted" {
		t.Fatalf("partial report lost: %+v", report)
	}
	record, err := m.ReadHistory(id)
	if err != nil {
		t.Fatal(err)
	}
	var saved Snapshot
	if err := json.Unmarshal(record.Data, &saved); err != nil {
		t.Fatal(err)
	}
	var savedReport compare.Report
	if err := json.Unmarshal(saved.Payload, &savedReport); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(savedReport, report) {
		t.Fatal("saved payload differs from live result")
	}
	trace, err := os.ReadFile(filepath.Join(dir, "output", id+".trace.jsonl"))
	if err != nil || !bytes.Contains(trace, []byte(`"lexeme":"package"`)) {
		t.Fatalf("trace lost: %v", err)
	}
}

func TestFinalTraceIsNeverOverwritten(t *testing.T) {
	dir := t.TempDir()
	entered := make(chan struct{}, 1)
	m := New(Config{OutputDir: dir, Resolve: func(ctx context.Context, _ compare.Model) (rank.Ranker, error) {
		entered <- struct{}{}
		<-ctx.Done()
		return nil, ctx.Err()
	}})
	id, err := m.Start(Request{Kind: "search", Engine: "token", SpecPath: testSpec(t), Model: compare.Model{ID: "fixture", Ranker: "jev"}})
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("resolver not entered")
	}
	path := filepath.Join(dir, id+".trace.jsonl")
	if err := os.WriteFile(path, []byte("existing trace"), 0600); err != nil {
		t.Fatal(err)
	}
	_ = m.Cancel()
	s := waitEnd(t, m)
	b, err := os.ReadFile(path)
	if err != nil || string(b) != "existing trace" || s.Status != "storage_error" {
		t.Fatalf("existing trace was lost/status wrong: %s %v", s.Status, err)
	}
}

func TestAllFortyWithoutUIConsumptionKeepsFinalState(t *testing.T) {
	manifest, err := filepath.Abs("../../examples/token/compare-live.json")
	if err != nil {
		t.Fatal(err)
	}
	m := New(Config{OutputDir: t.TempDir(), Resolve: fixtureResolver})
	_, err = m.Start(Request{Kind: "search", Engine: "token", ManifestPath: manifest, ProblemID: "all", Model: compare.Model{ID: "fixture", Ranker: "jev"}, Options: runner.Request{MaxCalls: 1}})
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		s := m.Snapshot()
		if s.Status != "running" && s.Status != "cancelling" {
			if s.Status != "completed" || s.Total != 40 || s.Completed != 40 || len(s.Results) != 40 {
				t.Fatalf("suite incomplete: %+v", s)
			}
			for _, result := range s.Results {
				if result.Result.Calls > 1 {
					t.Fatal("problem request budget not reset")
				}
			}
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	_ = m.Cancel()
	t.Fatal("slow/absent UI blocked suite")
}
