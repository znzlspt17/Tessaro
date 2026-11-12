package runner

import (
	"context"
	"math"
	"testing"

	"jevcancode/internal/rank"
)

type decisionRankerFixture struct{}

func (decisionRankerFixture) Rank(context.Context, string, []rank.Candidate) ([]rank.Score, error) {
	panic("decision interface was bypassed")
}
func (decisionRankerFixture) RankDecision(_ context.Context, _ string, _ []rank.Candidate) (rank.Decision, error) {
	i, o := 12, 2
	return rank.Decision{Scores: []rank.Score{{ID: "a", Score: .5}, {ID: "b", Score: .5}}, ChosenID: "b", Usage: &rank.Usage{InputTokens: &i, OutputTokens: &o}}, nil
}
func TestMeasuredRankerPreservesActualChoiceAndUsage(t *testing.T) {
	m := &measuredRanker{Ranker: decisionRankerFixture{}, inputKnown: true, outputKnown: true}
	d, err := m.RankDecision(context.Background(), "", []rank.Candidate{{ID: "a"}, {ID: "b"}})
	if err != nil || d.ChosenID != "b" || m.usage() == nil || *m.usage().InputTokens != 12 {
		t.Fatalf("decision=%+v usage=%+v err=%v", d, m.usage(), err)
	}
}

type batchUsageRanker struct{}

func (batchUsageRanker) Rank(context.Context, string, []rank.Candidate) ([]rank.Score, error) {
	panic("unexpected single")
}
func (batchUsageRanker) SupportsDecisionBatch() bool { return true }
func (batchUsageRanker) RankDecisions(_ context.Context, qs []rank.DecisionRequest) ([]rank.Decision, *rank.Usage, error) {
	ds := make([]rank.Decision, len(qs))
	for i, q := range qs {
		ss := make([]rank.Score, len(q.Candidates))
		for j, c := range q.Candidates {
			ss[j] = rank.Score{ID: c.ID, Score: float64(len(ss) - j)}
		}
		ds[i] = rank.Decision{Scores: ss}
	}
	in, out := 17, 4
	return ds, &rank.Usage{InputTokens: &in, OutputTokens: &out}, nil
}
func TestMeasuredBatchRecordsAggregateUsageOnce(t *testing.T) {
	m := &measuredRanker{Ranker: batchUsageRanker{}, inputKnown: true, outputKnown: true}
	batch, ok := any(m).(rank.BatchDecisionRanker)
	if !ok {
		t.Fatal("batch capability lost")
	}
	qs := []rank.DecisionRequest{{Candidates: []rank.Candidate{{ID: "a"}, {ID: "b"}}}, {Candidates: []rank.Candidate{{ID: "c"}, {ID: "d"}}}}
	ds, _, err := batch.RankDecisions(context.Background(), qs)
	u := m.usage()
	if err != nil || len(ds) != 2 || u == nil || u.InputTokens == nil || *u.InputTokens != 17 || m.calls != 2 {
		t.Fatalf("ds=%+v usage=%+v calls=%d err=%v", ds, u, m.calls, err)
	}
}

type rejectedBatchRanker struct{ batchUsageRanker }

func (rejectedBatchRanker) RankDecisions(context.Context, []rank.DecisionRequest) ([]rank.Decision, *rank.Usage, error) {
	return nil, nil, &rank.SafeError{Code: "question_budget_exhausted", Message: "budget"}
}
func TestMeasuredBatchDoesNotCountRejectedQuestions(t *testing.T) {
	m := &measuredRanker{Ranker: rejectedBatchRanker{}, inputKnown: true, outputKnown: true}
	qs := []rank.DecisionRequest{{Candidates: []rank.Candidate{{ID: "a"}, {ID: "b"}}}, {Candidates: []rank.Candidate{{ID: "c"}, {ID: "d"}}}}
	_, _, err := m.RankDecisions(context.Background(), qs)
	if err == nil || m.calls != 0 || m.usage() != nil {
		t.Fatalf("rejected batch counted: calls=%d usage=%+v err=%v", m.calls, m.usage(), err)
	}
}

func TestPartialKnownUsageDoesNotInventOtherTokenCounts(t *testing.T) {
	m := &measuredRanker{inputKnown: true, outputKnown: true, calls: 2}
	in := 7
	m.recordUsage(&rank.Usage{InputTokens: &in})
	m.recordUsage(nil)
	total, known := m.usage(), m.knownUsage()
	if total == nil || total.InputTokens != nil || total.OutputTokens != nil || known == nil || known.InputTokens == nil || *known.InputTokens != 7 || known.OutputTokens != nil {
		t.Fatalf("wrong totals: total=%+v known=%+v", total, known)
	}
	result := Result{}
	price := 1.0
	result.recordUsage(m, &price, nil)
	if result.EstimatedCost != nil || result.KnownEstimatedCost == nil || math.Abs(*result.KnownEstimatedCost-.000007) > 1e-12 {
		t.Fatalf("partial cost: %+v", result)
	}
}
