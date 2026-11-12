package application

import (
	"context"
	"encoding/json"
	"os"
	"testing"
	"time"

	"tessaro/internal/compare"
	"tessaro/internal/rank"
	"tessaro/internal/runner"
	"tessaro/internal/tokensynth"
)

func TestDecisionResultVisibleAndClearedForNewRequest(t *testing.T) {
	m := New(Config{OutputDir: t.TempDir()})
	f, err := os.CreateTemp(t.TempDir(), "journal")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	m.journal = f
	m.nodes = map[string]node{}
	var result tokensynth.Event
	if err := json.Unmarshal([]byte(`{"reason":"decision_result","chosen_id":"b","decision_status":"received"}`), &result); err != nil {
		t.Fatal(err)
	}
	m.tokenEvent(result)
	b, _ := json.Marshal(m.Snapshot())
	var got map[string]any
	_ = json.Unmarshal(b, &got)
	if got["chosen_id"] != "b" || got["decision_status"] != "received" {
		t.Fatalf("result not reflected: %s", b)
	}
	m.tokenEvent(tokensynth.Event{Reason: "decision_context"})
	b, _ = json.Marshal(m.Snapshot())
	got = nil
	_ = json.Unmarshal(b, &got)
	if got["chosen_id"] != nil || got["decision_status"] != "prepared" {
		t.Fatalf("stale selection: %s", b)
	}
	m.tokenEvent(tokensynth.Event{ID: "forced", ParentID: "root", Lexeme: "return", Reason: "forced", Step: 1})
	b, _ = json.Marshal(m.Snapshot())
	got = nil
	_ = json.Unmarshal(b, &got)
	if got["decision_status"] != "forced" || got["forced_id"] != "forced" || got["forced_lexeme"] != "return" {
		t.Fatalf("forced decision missing: %s", b)
	}
	m.tokenEvent(tokensynth.Event{ID: "option", ParentID: "forced", Lexeme: "a", Reason: "available", Step: 1})
	b, _ = json.Marshal(m.Snapshot())
	got = nil
	_ = json.Unmarshal(b, &got)
	rows := got["candidates"].([]any)
	if got["forced_id"] != nil || len(rows) != 1 || rows[0].(map[string]any)["id"] != "option" {
		t.Fatalf("forced step leaked into next request: %s", b)
	}
}

type tiedDecisionRanker struct{}

func (tiedDecisionRanker) Rank(ctx context.Context, state string, cs []rank.Candidate) ([]rank.Score, error) {
	d, err := (tiedDecisionRanker{}).RankDecision(ctx, state, cs)
	return d.Scores, err
}
func (tiedDecisionRanker) RankDecision(_ context.Context, _ string, cs []rank.Candidate) (rank.Decision, error) {
	scores := make([]rank.Score, len(cs))
	for i, c := range cs {
		scores[i] = rank.Score{ID: c.ID, Score: .5}
	}
	return rank.Decision{Scores: scores, ChosenID: cs[len(cs)-1].ID}, nil
}
func TestActualDecisionSurvivesRunAndTrace(t *testing.T) {
	m := New(Config{OutputDir: t.TempDir(), Resolve: func(context.Context, compare.Model) (rank.Ranker, error) { return tiedDecisionRanker{}, nil }})
	id, err := m.Start(Request{Kind: "search", Engine: "token", SpecPath: testSpec(t), ProblemID: "factorial", Model: compare.Model{ID: "test", Ranker: "local"}, Options: runner.Request{MaxCalls: 1}})
	if err != nil {
		t.Fatal(err)
	}
	waitEnd(t, m)
	events, err := m.ReadTrace(id)
	if err != nil {
		t.Fatal(err)
	}
	for _, envelope := range events {
		if envelope.Kind != "search" {
			continue
		}
		var ev tokensynth.Event
		if json.Unmarshal(envelope.Data, &ev) == nil && ev.Reason == "decision_result" {
			if ev.DecisionStatus != "received" || ev.ChosenID == "" {
				t.Fatalf("invalid recorded decision: %+v", ev)
			}
			return
		}
	}
	t.Fatal("choice was lost before trace persistence")
}

func TestForcedBranchPreservesOtherParentCandidatesInSameStep(t *testing.T) {
	m := New(Config{OutputDir: t.TempDir()})
	f, err := os.CreateTemp(t.TempDir(), "journal")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	m.journal = f
	m.nodes = map[string]node{}
	m.tokenEvent(tokensynth.Event{ID: "a", ParentID: "pa", Lexeme: "+", Reason: "available", Step: 2})
	m.tokenEvent(tokensynth.Event{ID: "rejected", ParentID: "pa", Lexeme: ";", Reason: "body_rejected", Step: 2})
	m.tokenEvent(tokensynth.Event{ID: "forced", ParentID: "pb", Lexeme: "return", Reason: "forced", Step: 2})
	m.tokenEvent(tokensynth.Event{ID: "b", ParentID: "forced", Lexeme: "x", Reason: "available", Step: 2})
	ids := map[string]bool{}
	for _, c := range m.Snapshot().Candidates {
		ids[c.ID] = true
	}
	if !ids["a"] || !ids["rejected"] || !ids["b"] {
		t.Fatalf("same-step candidate lost: %v", ids)
	}
}

func TestCancelledCallRecordsFailedDecisionWithoutChoice(t *testing.T) {
	r := &blockingRanker{entered: make(chan struct{}, 1)}
	m := New(Config{OutputDir: t.TempDir(), Resolve: func(context.Context, compare.Model) (rank.Ranker, error) { return r, nil }})
	id, err := m.Start(Request{Kind: "search", Engine: "token", SpecPath: testSpec(t), ProblemID: "factorial", Model: compare.Model{ID: "test", Ranker: "local"}})
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-r.entered:
	case <-time.After(10 * time.Second):
		t.Fatal("model call did not start")
	}
	if err := m.Cancel(); err != nil {
		t.Fatal(err)
	}
	waitEnd(t, m)
	events, err := m.ReadTrace(id)
	if err != nil {
		t.Fatal(err)
	}
	for _, envelope := range events {
		if envelope.Kind != "search" {
			continue
		}
		var event tokensynth.Event
		if json.Unmarshal(envelope.Data, &event) == nil && event.Reason == "decision_result" {
			if event.DecisionStatus != "failed" || event.ChosenID != "" {
				t.Fatalf("cancelled decision: %+v", event)
			}
			return
		}
	}
	t.Fatal("cancelled decision was not recorded")
}

type researchDecisionRanker struct{}

func (researchDecisionRanker) NormalizedCategorical() bool { return true }
func (researchDecisionRanker) Rank(ctx context.Context, state string, cs []rank.Candidate) ([]rank.Score, error) {
	d, e := (researchDecisionRanker{}).RankDecision(ctx, state, cs)
	return d.Scores, e
}
func (researchDecisionRanker) RankDecision(_ context.Context, _ string, cs []rank.Candidate) (rank.Decision, error) {
	s := make([]rank.Score, len(cs))
	for i, c := range cs {
		s[i] = rank.Score{ID: c.ID, Score: 1 / float64(len(cs))}
	}
	return rank.Decision{Scores: s, ChosenID: cs[len(cs)-1].ID}, nil
}
func TestResearchDecisionsPersistDistinctStepsAndChoices(t *testing.T) {
	m := New(Config{OutputDir: t.TempDir(), Resolve: func(context.Context, compare.Model) (rank.Ranker, error) { return researchDecisionRanker{}, nil }})
	id, e := m.Start(Request{Kind: "search", Engine: "token", Strategy: "lds", SpecPath: testSpec(t), Model: compare.Model{ID: "test", Ranker: "local"}, Options: runner.Request{MaxCalls: 3}})
	if e != nil {
		t.Fatal(e)
	}
	waitEnd(t, m)
	events, e := m.ReadTrace(id)
	if e != nil {
		t.Fatal(e)
	}
	steps := map[int]bool{}
	lastStep := 0
	for _, env := range events {
		var ev tokensynth.Event
		if env.Kind != "search" || json.Unmarshal(env.Data, &ev) != nil {
			continue
		}
		if ev.Reason == "decision_result" {
			if ev.Step <= lastStep || ev.ChosenID == "" || ev.DecisionStatus != "received" {
				t.Fatalf("invalid decision lifecycle: %+v", ev)
			}
			steps[ev.Step] = true
			lastStep = ev.Step
		}
	}
	if len(steps) < 2 {
		t.Fatalf("expected multiple distinct decisions: %+v", steps)
	}
}
