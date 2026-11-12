package benchmark

import (
	"context"
	"errors"
	"tessaro/internal/rank"
	"strings"
	"testing"
)

type broken struct{}

type cancelRanker struct {
	cancel context.CancelFunc
	calls  int
}

func (r *cancelRanker) Rank(context.Context, string, []rank.Candidate) ([]rank.Score, error) {
	r.calls++
	r.cancel()
	return nil, errors.New("unsafe diagnostic")
}
func TestCancelledRunDoesNotStartFurtherRequestsOrSaveDiagnostics(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	r := &cancelRanker{cancel: cancel}
	m := Manifest{Problems: []Problem{{ID: "one"}, {ID: "two"}}}
	out := Run(ctx, m, r)
	if r.calls != 1 || len(out.Items) != 2 || out.Items[0].Error != "model_error" || out.Items[1].Error != "not_run_cancelled" {
		t.Fatalf("calls %d, items %+v", r.calls, out.Items)
	}
}

func (broken) Rank(context.Context, string, []rank.Candidate) ([]rank.Score, error) {
	return nil, errors.New("failed")
}
func TestFailureIncludedInDenominator(t *testing.T) {
	m := Manifest{Problems: []Problem{{ID: "p", Intent: "x", Candidates: []rank.Candidate{{ID: "a"}, {ID: "b"}, {ID: "c"}, {ID: "d"}}, Answers: []string{"a"}}}}
	r := Run(context.Background(), m, broken{})
	if r.Total != 1 || r.Failed != 1 || r.Top1 != 0 {
		t.Fatalf("%+v", r)
	}
}
func TestMultipleAnswers(t *testing.T) {
	m := Manifest{Problems: []Problem{{ID: "p", Intent: "x", Candidates: []rank.Candidate{{ID: "a"}, {ID: "b"}, {ID: "c"}, {ID: "d"}}, Answers: []string{"b", "c"}}}}
	r := Run(context.Background(), m, rank.Fake{})
	if r.Top1 != 0 || r.MRR != 0.5 || r.Items[0].Rank != 2 {
		t.Fatalf("%+v", r)
	}
}
func TestTop3RecallCountsAllCorrectAnswers(t *testing.T) {
	m := Manifest{Problems: []Problem{{ID: "p", Intent: "x", Candidates: []rank.Candidate{{ID: "a"}, {ID: "b"}, {ID: "c"}, {ID: "d"}}, Answers: []string{"a", "c", "d"}}}}
	r := Run(context.Background(), m, rank.Fake{})
	if r.Top3Recall != 2.0/3.0 {
		t.Fatalf("recall %v", r.Top3Recall)
	}
}

type capture struct{ state string }

func (c *capture) Rank(_ context.Context, state string, cs []rank.Candidate) ([]rank.Score, error) {
	c.state = state
	return rank.Fake{}.Rank(context.Background(), state, cs)
}
func TestPrefixIncludedAndAnswersHidden(t *testing.T) {
	m := Manifest{Problems: []Problem{{ID: "p", Intent: "complete", CodePrefix: "func F()", Candidates: []rank.Candidate{{ID: "a"}, {ID: "b"}, {ID: "c"}, {ID: "d"}}, Answers: []string{"answer-secret"}}}}
	r := &capture{}
	_ = Run(context.Background(), m, r)
	if !strings.Contains(r.state, "func F()") || strings.Contains(r.state, "answer-secret") {
		t.Fatalf("unexpected state: %s", r.state)
	}
}
func TestEvaluationKind(t *testing.T) {
	r := Run(context.Background(), Manifest{}, rank.Fake{})
	if r.EvaluationKind != "complete_candidate_selection" {
		t.Fatalf("kind %q", r.EvaluationKind)
	}
}
