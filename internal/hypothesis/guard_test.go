package hypothesis

import (
	"context"
	"errors"
	"jevcancode/internal/rank"
	"testing"
)

type fixtureRanker struct {
	model string
	calls int
}

func (f *fixtureRanker) Metadata() rank.Metadata {
	return rank.Metadata{Model: "jev-1.14.0", ReturnedModel: f.model}
}
func (f *fixtureRanker) Rank(ctx context.Context, s string, cs []rank.Candidate) ([]rank.Score, error) {
	d, e := f.RankDecision(ctx, s, cs)
	return d.Scores, e
}
func (f *fixtureRanker) RankDecision(_ context.Context, _ string, cs []rank.Candidate) (rank.Decision, error) {
	f.calls++
	return rank.Decision{ChosenID: cs[0].ID, Scores: []rank.Score{{ID: cs[0].ID, Score: 1}}}, nil
}
func TestGuardStopsAtRequestLimit(t *testing.T) {
	f := &fixtureRanker{model: "jev-1.14.0"}
	g := &guardedRanker{inner: f, limit: 1}
	cs := []rank.Candidate{{ID: "x"}}
	if _, e := g.RankDecision(context.Background(), "", cs); e != nil {
		t.Fatal(e)
	}
	if _, e := g.RankDecision(context.Background(), "", cs); !errors.Is(e, errRequestBudget) || f.calls != 1 {
		t.Fatalf("error %v calls %d", e, f.calls)
	}
}
func TestGuardStopsOnReturnedModelMismatch(t *testing.T) {
	f := &fixtureRanker{model: "different"}
	g := &guardedRanker{inner: f, limit: 3}
	cs := []rank.Candidate{{ID: "x"}}
	if _, e := g.RankDecision(context.Background(), "", cs); !errors.Is(e, errModelMismatch) {
		t.Fatalf("error %v", e)
	}
	if _, e := g.RankDecision(context.Background(), "", cs); !errors.Is(e, errModelMismatch) || f.calls != 1 {
		t.Fatalf("error %v calls %d", e, f.calls)
	}
}

func TestGuardUnknownUsageStaysUnknown(t *testing.T) {
	f := &fixtureRanker{model: "jev-1.14.0"}
	g := &guardedRanker{inner: f, limit: 1}
	_, e := g.RankDecision(context.Background(), "", []rank.Candidate{{ID: "x"}})
	if e != nil {
		t.Fatal(e)
	}
	if g.usage() == nil || g.usage().InputTokens != nil || g.usage().OutputTokens != nil {
		t.Fatalf("usage %+v", g.usage())
	}
}
