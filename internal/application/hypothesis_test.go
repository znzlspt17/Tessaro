package application

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"tessaro/internal/compare"
	"tessaro/internal/rank"
)

type cancellingHypothesisRanker struct {
	calls   atomic.Int32
	entered chan struct{}
}

func (r *cancellingHypothesisRanker) Metadata() rank.Metadata {
	return rank.Metadata{ReturnedModel: "jev-1.14.0"}
}
func (r *cancellingHypothesisRanker) Rank(ctx context.Context, state string, cs []rank.Candidate) ([]rank.Score, error) {
	d, e := r.RankDecision(ctx, state, cs)
	return d.Scores, e
}
func (r *cancellingHypothesisRanker) RankDecision(ctx context.Context, _ string, cs []rank.Candidate) (rank.Decision, error) {
	if r.calls.Add(1) == 2 {
		close(r.entered)
		<-ctx.Done()
		return rank.Decision{}, ctx.Err()
	}
	scores := make([]rank.Score, len(cs))
	for i, c := range cs {
		scores[i] = rank.Score{ID: c.ID, Score: 0.5}
	}
	return rank.Decision{Scores: scores, ChosenID: cs[0].ID}, nil
}

func TestHypothesisCancellationPreservesObservedRanking(t *testing.T) {
	dir := t.TempDir()
	files := map[string]string{
		"p.json":        `{"intent":"identity","inputs":["a"],"allowed_types":["int"],"public_tests":[{"inputs":{"a":1},"want":1}]}`,
		"refs.json":     `[{"id":"p","spec":"p.json","source":"package main; func f0(a int) int { return a }"}]`,
		"manifest.json": `{"schema_version":1,"model":{"id":"jev","ranker":"jev","model":"jev-1.14.0"},"references":"refs.json","problems":[{"id":"p","spec":"p.json"}],"seed":42,"budgets":{"request_limit":10,"samples_per_problem":1,"max_calls":64,"max_tokens":256,"max_candidates":8192,"timeout_seconds":60}}`,
	}
	for name, data := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
	}
	r := &cancellingHypothesisRanker{entered: make(chan struct{})}
	m := New(Config{OutputDir: filepath.Join(dir, "output"), Resolve: func(context.Context, compare.Model) (rank.Ranker, error) { return r, nil }})
	id, err := m.Start(Request{Kind: "hypothesis", ManifestPath: filepath.Join(dir, "manifest.json")})
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-r.entered:
	case <-time.After(30 * time.Second):
		t.Fatalf("second request not reached: %+v", m.Snapshot())
	}
	if err := m.Cancel(); err != nil {
		t.Fatal(err)
	}
	s := waitEnd(t, m)
	var report struct {
		RequestsUsed int `json:"requests_used"`
		Ranking      struct {
			Observations []json.RawMessage `json:"observations"`
		} `json:"ranking"`
		Summary string `json:"summary"`
	}
	if err := json.Unmarshal(s.Payload, &report); err != nil || s.Status != "cancelled" || report.RequestsUsed < 2 || len(report.Ranking.Observations) != 1 || report.Summary == "" {
		t.Fatalf("snapshot=%+v report=%+v err=%v", s, report, err)
	}
	stored, err := m.ReadHistory(id)
	if err != nil || stored.Status != "cancelled" || !strings.Contains(string(stored.Data), `"observations"`) {
		t.Fatalf("history=%+v err=%v", stored, err)
	}
	b, err := os.ReadFile(filepath.Join(dir, "output", id+".summary.md"))
	if err != nil || string(b) != report.Summary {
		t.Fatalf("summary=%q err=%v", b, err)
	}
	events, err := m.ReadTrace(id)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, event := range events {
		if event.Kind == "hypothesis_detail" && strings.Contains(string(event.Data), "original_decision") {
			found = true
		}
	}
	if !found {
		t.Fatal("first ranking observation missing from trace")
	}
}

func TestHypothesisJobAcceptsKind(t *testing.T) {
	m := New(Config{OutputDir: t.TempDir(), Resolve: func(context.Context, compare.Model) (rank.Ranker, error) { return nil, nil }})
	_, err := m.Start(Request{Kind: "hypothesis", ManifestPath: "missing.json"})
	if err != nil {
		t.Fatalf("hypothesis job rejected: %v", err)
	}
	waitEnd(t, m)
}

func TestHypothesisInvalidInputPreservesReportAndSanitizesError(t *testing.T) {
	dir := t.TempDir()
	m := New(Config{OutputDir: dir, Resolve: fixtureResolver})
	id, err := m.Start(Request{Kind: "hypothesis", ManifestPath: filepath.Join(dir, "sensitive-name")})
	if err != nil {
		t.Fatal(err)
	}
	s := waitEnd(t, m)
	if s.Status != "failed" || s.Error != "execution_error" || strings.Contains(string(s.Payload), "sensitive-name") {
		t.Fatalf("unsafe or incorrect result: %+v", s)
	}
	var report struct {
		Status  string `json:"status"`
		Summary string `json:"summary"`
	}
	if err := json.Unmarshal(s.Payload, &report); err != nil || report.Status != "input_error" {
		t.Fatalf("missing partial report: %s, %v", s.Payload, err)
	}
	b, err := os.ReadFile(filepath.Join(dir, id+".summary.md"))
	if err != nil || string(b) != report.Summary {
		t.Fatalf("summary=%q err=%v", b, err)
	}
	r, err := m.ReadHistory(id)
	if err != nil || r.Status != "failed" || !strings.Contains(string(r.Data), `"input_error"`) {
		t.Fatalf("history=%+v err=%v", r, err)
	}
}

func TestHypothesisSummaryWriteFailureMarksStorageFailure(t *testing.T) {
	dir := t.TempDir()
	m := New(Config{OutputDir: dir, Resolve: fixtureResolver})
	m.path = filepath.Join(dir, "result")
	if err := os.Mkdir(m.path+".summary.md", 0700); err != nil {
		t.Fatal(err)
	}
	f, err := os.CreateTemp(dir, "journal")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	m.journal = f
	_ = m.hypothesis(context.Background(), Request{ManifestPath: filepath.Join(dir, "missing")})
	if !m.storageFailed || len(m.snapshot.Payload) == 0 {
		t.Fatalf("storage failure or report missing: %+v", m.snapshot)
	}
}

func TestHypothesisCancellationRetainsPartialReport(t *testing.T) {
	dir := t.TempDir()
	m := New(Config{OutputDir: dir, Resolve: fixtureResolver})
	manifest, err := filepath.Abs("../../examples/token/hypothesis.json")
	if err != nil {
		t.Fatal(err)
	}
	id, err := m.Start(Request{Kind: "hypothesis", ManifestPath: manifest})
	if err != nil {
		t.Fatal(err)
	}
	if err := m.Cancel(); err != nil {
		t.Fatal(err)
	}
	s := waitEnd(t, m)
	if s.Status != "cancelled" || len(s.Payload) == 0 {
		t.Fatalf("snapshot=%+v", s)
	}
	r, err := m.ReadHistory(id)
	if err != nil || r.Status != "cancelled" || !strings.Contains(string(r.Data), `"payload"`) {
		t.Fatalf("history=%+v err=%v", r, err)
	}
	if _, err := os.Stat(filepath.Join(dir, id+".summary.md")); err != nil {
		t.Fatal(err)
	}
}
