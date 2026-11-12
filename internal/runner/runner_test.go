package runner

import (
	"context"
	"jevcancode/internal/rank"
	"jevcancode/internal/spec"
	"testing"
)

func TestHeldoutNotLoadedOnSearchFailure(t *testing.T) {
	p := spec.Problem{Intent: "x", Name: "F", ReturnType: "int", Constants: []int{0}, PublicTests: []spec.Test{{Want: 1}}}
	called := false
	r := Run(context.Background(), p, rank.Fake{}, Request{Strategy: "strict-greedy", MaxCandidates: 1, Heldout: func() ([]spec.Test, error) { called = true; return nil, nil }})
	if called || r.Status != "budget_exhausted" {
		t.Fatalf("called=%v result=%+v", called, r)
	}
}

func TestCancellationDuringHeldoutKeepsSubmissionAndReason(t *testing.T) {
	p := spec.Problem{Intent: "identity", Name: "F", Params: []spec.Param{{Name: "x", Type: "int"}}, ReturnType: "int", PublicTests: []spec.Test{{Args: []any{1}, Want: 1}}}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	r := Run(ctx, p, rank.Fake{}, Request{Strategy: "strict-greedy", Heldout: func() ([]spec.Test, error) {
		cancel()
		return []spec.Test{{Args: []any{2}, Want: 2}}, nil
	}})
	if r.Status != "cancelled" || !r.PublicPassed || r.Source == "" || r.Calls == 0 {
		t.Fatalf("lost cancellation or submission: %+v", r)
	}
}
