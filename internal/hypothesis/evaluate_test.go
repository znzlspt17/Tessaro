package hypothesis

import (
	"context"
	"errors"
	"jevcancode/internal/compare"
	"jevcancode/internal/rank"
	"os"
	"path/filepath"
	"testing"
)

type bFixture struct{ states []string }

func (f *bFixture) Metadata() rank.Metadata {
	return rank.Metadata{Model: "jev-1.14.0", ReturnedModel: "jev-1.14.0"}
}
func (f *bFixture) Rank(ctx context.Context, s string, cs []rank.Candidate) ([]rank.Score, error) {
	d, e := f.RankDecision(ctx, s, cs)
	return d.Scores, e
}
func (f *bFixture) RankDecision(_ context.Context, s string, cs []rank.Candidate) (rank.Decision, error) {
	f.states = append(f.states, s)
	scores := make([]rank.Score, len(cs))
	for i, c := range cs {
		scores[i] = rank.Score{ID: c.ID, Score: float64(len(cs)-i) / float64(len(cs))}
	}
	return rank.Decision{ChosenID: cs[0].ID, Scores: scores}, nil
}
func TestBUsesCapturedStateForBothIDConditions(t *testing.T) {
	f := &bFixture{}
	g := &guardedRanker{inner: f, limit: 2}
	c := capturedDecision{State: `{"intent":"visible"}`, Candidates: []rank.Candidate{{ID: "a", Lexeme: "x"}, {ID: "b", Lexeme: "y"}}, ValidIDs: []string{"a"}}
	var events []Progress
	m := manifest{Seed: 42, Budgets: budgets{Samples: 48}}
	result, e := runB(context.Background(), map[string][]capturedDecision{"p": {c}}, []string{"p"}, m, g, func(p Progress) { events = append(events, p) })
	if e != nil || len(result.Observations) != 2 || len(f.states) != 2 || f.states[0] != c.State || f.states[1] != c.State || len(events) < 2 {
		t.Fatalf("result %+v states %v events %d error %v", result, f.states, len(events), e)
	}
}

func TestRunPreservesBudgetExhaustedPartialResults(t *testing.T) {
	dir := t.TempDir()
	spec, e := os.ReadFile("../../examples/token/specs/arithmetic-add.json")
	if e != nil {
		t.Fatal(e)
	}
	if e = os.WriteFile(filepath.Join(dir, "spec.json"), spec, 0600); e != nil {
		t.Fatal(e)
	}
	refs := `[{"id":"p","spec":"spec.json","source":"package main\nfunc f0(a int, b int) int { return a + b }"}]`
	manifest := `{"schema_version":1,"model":{"id":"jev","ranker":"jev","model":"jev-1.14.0"},"references":"refs.json","problems":[{"id":"p","spec":"spec.json"}],"seed":42,"budgets":{"request_limit":1,"samples_per_problem":1,"max_calls":64,"max_tokens":256,"max_candidates":8192,"timeout_seconds":300}}`
	if e = os.WriteFile(filepath.Join(dir, "refs.json"), []byte(refs), 0600); e != nil {
		t.Fatal(e)
	}
	path := filepath.Join(dir, "manifest.json")
	if e = os.WriteFile(path, []byte(manifest), 0600); e != nil {
		t.Fatal(e)
	}
	f := &bFixture{}
	report, e := Run(context.Background(), path, func(context.Context, compare.Model) (rank.Ranker, error) { return f, nil }, nil)
	if e != nil || report.Status != "budget_exhausted" || report.RequestsUsed != 1 || report.Ranking == nil || len(report.Ranking.Observations) != 1 {
		t.Fatalf("status %s used %d error %v", report.Status, report.RequestsUsed, e)
	}
}

func tinyManifest(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	spec, e := os.ReadFile("../../examples/token/specs/arithmetic-add.json")
	if e != nil {
		t.Fatal(e)
	}
	if e = os.WriteFile(filepath.Join(dir, "spec.json"), spec, 0600); e != nil {
		t.Fatal(e)
	}
	refs := `[{"id":"p","spec":"spec.json","source":"package main\nfunc f0(a int, b int) int { return a + b }"}]`
	manifest := `{"schema_version":1,"model":{"id":"jev","ranker":"jev","model":"jev-1.14.0"},"references":"refs.json","problems":[{"id":"p","spec":"spec.json","heldout":"heldout.json"}],"seed":42,"budgets":{"request_limit":100,"samples_per_problem":1,"max_calls":1,"max_tokens":256,"max_candidates":8192,"timeout_seconds":300}}`
	if e = os.WriteFile(filepath.Join(dir, "refs.json"), []byte(refs), 0600); e != nil {
		t.Fatal(e)
	}
	path := filepath.Join(dir, "manifest.json")
	if e = os.WriteFile(path, []byte(manifest), 0600); e != nil {
		t.Fatal(e)
	}
	return path
}
func TestRunCancellationKeepsCapturedPartialReport(t *testing.T) {
	path := tinyManifest(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	f := &bFixture{}
	report, e := Run(ctx, path, func(context.Context, compare.Model) (rank.Ranker, error) { return f, nil }, func(p Progress) {
		if p.Phase == "ranking" {
			cancel()
		}
	})
	if !errors.Is(e, context.Canceled) || report.Status != "cancelled" || report.Ranking == nil || len(report.Problems) != 1 || len(f.states) != 0 {
		t.Fatalf("status %s err %v ranking %+v", report.Status, e, report.Ranking)
	}
}
func TestRunAllStagesWithFixture(t *testing.T) {
	path := tinyManifest(t)
	f := &bFixture{}
	events := 0
	report, e := Run(context.Background(), path, func(context.Context, compare.Model) (rank.Ranker, error) { return f, nil }, func(p Progress) {
		if p.Event != nil {
			events++
		}
	})
	if e != nil || report.Status != "completed" || len(report.Ranking.Observations) != 2 || len(report.Generation) != 8 || events == 0 {
		t.Fatalf("status %s err %v runs %d events %d", report.Status, e, len(report.Generation), events)
	}
}
