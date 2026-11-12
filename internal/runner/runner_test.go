package runner

import (
	"context"
	"jevcancode/internal/rank"
	"jevcancode/internal/spec"
	"jevcancode/internal/verify"
	"strings"
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

type astPolicyProbe struct {
	computedReturn bool
	assignment     bool
}

func (r *astPolicyProbe) Rank(_ context.Context, _ string, cs []rank.Candidate) ([]rank.Score, error) {
	ss := make([]rank.Score, len(cs))
	for i, c := range cs {
		if strings.Contains(c.Code, "return (") {
			r.computedReturn = true
		}
		if strings.Contains(c.Code, ":= (") {
			r.assignment = true
		}
		ss[i] = rank.Score{ID: c.ID, Score: float64(len(cs) - i)}
	}
	return ss, nil
}
func TestASTRunnerAppliesReturnPolicy(t *testing.T) {
	p := spec.Problem{Intent: "sum", Name: "Add", Params: []spec.Param{{Name: "x", Type: "int"}, {Name: "y", Type: "int"}}, ReturnType: "int", PublicTests: []spec.Test{{Args: []any{1, 2}, Want: 3}}}
	legacy := &astPolicyProbe{}
	_ = Run(context.Background(), p, legacy, Request{Strategy: "beam", MaxCandidates: 100, MaxCalls: 30})
	constrained := &astPolicyProbe{}
	out := Run(context.Background(), p, constrained, Request{Strategy: "beam", ReturnPolicy: spec.ReturnPolicyValuesOnly, MaxCandidates: 100, MaxCalls: 30})
	if !legacy.computedReturn || constrained.computedReturn || !constrained.assignment || out.ReturnPolicy != spec.ReturnPolicyValuesOnly {
		t.Fatalf("legacy=%+v constrained=%+v out=%+v", legacy, constrained, out)
	}
	p.ReturnPolicy = spec.ReturnPolicyValuesOnly
	if _, err := verify.Source(p, "v0 := x + y; return v0"); err != nil {
		t.Fatalf("separate calculation and return rejected: %v", err)
	}
	if _, err := verify.Source(p, "return x + y"); err == nil {
		t.Fatal("computed return accepted under values-only")
	}
}

func TestASTRunnerRejectsUnsupportedBatch(t *testing.T) {
	p := spec.Problem{Intent: "identity", Name: "Identity", Params: []spec.Param{{Name: "x", Type: "int"}}, ReturnType: "int", PublicTests: []spec.Test{{Args: []any{1}, Want: 1}}}
	out := Run(context.Background(), p, rank.Fake{}, Request{Strategy: "beam", DecisionBatchSize: 2, MaxCandidates: 1})
	if out.Status != "input_error" {
		t.Fatalf("unsupported AST batch silently ignored: %+v", out)
	}
}
