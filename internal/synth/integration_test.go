package synth_test

import (
	"context"
	"jevcancode/internal/rank"
	"jevcancode/internal/spec"
	"jevcancode/internal/synth"
	"jevcancode/internal/verify"
	"strings"
	"testing"
	"time"
)

type targetRanker string

func (r targetRanker) Rank(_ context.Context, _ string, cs []rank.Candidate) ([]rank.Score, error) {
	out := make([]rank.Score, len(cs))
	for i, c := range cs {
		n := 0
		for n < len(c.Code) && n < len(r) && c.Code[n] == r[n] {
			n++
		}
		out[i] = rank.Score{ID: c.ID, Score: float64(n)}
	}
	return out, nil
}
func TestBooleanIfSynthesisExecutes(t *testing.T) {
	p := spec.Problem{Intent: "conditional identity", Name: "Choose", Params: []spec.Param{{Name: "flag", Type: "bool"}}, ReturnType: "bool", PublicTests: []spec.Test{{Args: []any{true}, Want: true}, {Args: []any{false}, Want: false}}}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	r, err := synth.Search(ctx, p, targetRanker("if flag { return true } else { return false }"), synth.Options{Beam: 1, MaxCandidates: 300, MaxCalls: 30}, func(body string) (bool, error) {
		if !strings.HasPrefix(body, "if ") {
			return false, nil
		}
		return verify.Check(ctx, p, body, p.PublicTests)
	})
	if err != nil || !strings.HasPrefix(r.Code, "if ") {
		t.Fatalf("%+v %v", r, err)
	}
}
func TestSynthesisFindsLocalAssignment(t *testing.T) {
	p := spec.Problem{Intent: "increment", Name: "Inc", Params: []spec.Param{{Name: "x", Type: "int"}}, ReturnType: "int", Constants: []int{1}, PublicTests: []spec.Test{{Args: []any{1}, Want: 2}}}
	target := "v0 := x; v0 = (v0 + 1); return v0"
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	r, err := synth.Search(ctx, p, targetRanker(target), synth.Options{Beam: 1, MaxCandidates: 1000, MaxCalls: 80}, func(body string) (bool, error) { return body == target, nil })
	if err != nil || r.Code != target {
		t.Fatalf("%+v %v", r, err)
	}
}

type mixedRanker string

func (r mixedRanker) Rank(ctx context.Context, intent string, cs []rank.Candidate) ([]rank.Score, error) {
	scores, err := targetRanker(r).Rank(ctx, intent, cs)
	for i, c := range cs {
		if strings.HasPrefix(c.Code, "v0 := x; if ") && strings.Contains(c.Code, "}; <stmt") {
			scores[i].Score += 0.1
		}
	}
	return scores, err
}
func TestMixedReturningBranchAndContinuation(t *testing.T) {
	p := spec.Problem{Intent: "if flag use x else increment", Name: "F", Params: []spec.Param{{Name: "flag", Type: "bool"}, {Name: "x", Type: "int"}}, ReturnType: "int", Constants: []int{1}, PublicTests: []spec.Test{{Args: []any{true, 2}, Want: 2}, {Args: []any{false, 2}, Want: 3}}}
	target := "v0 := x; if flag { return x } else { v0 = (x + 1);  }; return v0"
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	r, err := synth.Search(ctx, p, mixedRanker(target), synth.Options{Beam: 1, MaxCandidates: 1500, MaxCalls: 150}, func(body string) (bool, error) {
		if body != target {
			return false, nil
		}
		return verify.Check(ctx, p, body, p.PublicTests)
	})
	if err != nil || r.Code != target {
		t.Fatalf("%+v %v", r, err)
	}
}

func TestGeneratedLocalNamesAvoidParametersAndFunction(t *testing.T) {
	for _, tc := range []struct{ function, parameter string }{{"F", "v0"}, {"v0", "x"}} {
		t.Run(tc.function+"_"+tc.parameter, func(t *testing.T) {
			p := spec.Problem{Intent: "increment", Name: tc.function, Params: []spec.Param{{Name: tc.parameter, Type: "int"}}, ReturnType: "int", Constants: []int{1}, PublicTests: []spec.Test{{Args: []any{2}, Want: 3}}}
			target := "v1 := " + tc.parameter + "; v1 = (v1 + 1); return v1"
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			result, err := synth.Search(ctx, p, targetRanker(target), synth.Options{Beam: 1, MaxCandidates: 1000, MaxCalls: 80}, func(body string) (bool, error) {
				if body != target {
					return false, nil
				}
				return verify.Check(ctx, p, body, p.PublicTests)
			})
			if err != nil || result.Code != target {
				t.Fatalf("local collision: code=%q error=%v", result.Code, err)
			}
		})
	}
}
