package synth

import (
	"context"
	"errors"
	"jevcancoding/internal/rank"
	"jevcancoding/internal/spec"
	"strings"
	"testing"
)

func TestBeamSearchFindsIdentity(t *testing.T) {
	p := spec.Problem{Intent: "identity", Name: "Identity", Params: []spec.Param{{Name: "x", Type: "int"}}, ReturnType: "int", PublicTests: []spec.Test{{Args: []any{1}, Want: 1}}}
	r, e := Search(context.Background(), p, rank.Fake{}, Options{Beam: 3, MaxCandidates: 30, MaxCalls: 30}, func(code string) (bool, error) { return code == "return x", nil })
	if e != nil || r.Code != "return x" {
		t.Fatalf("got %#v %v", r, e)
	}
}
func TestBooleanNotIsGenerated(t *testing.T) {
	p := spec.Problem{Intent: "negate", Name: "Not", Params: []spec.Param{{Name: "flag", Type: "bool"}}, ReturnType: "bool", PublicTests: []spec.Test{{Args: []any{true}, Want: false}}}
	opts := choices(hole("bool", 0), p, 4)
	found := false
	for _, n := range opts {
		if n.kind == "unary" {
			*n.children[0] = node{kind: "symbol", symbolID: 0, text: "flag"}
			src, err := astSource(p, &node{kind: "return", children: []*node{n}})
			if err != nil || !strings.Contains(src, "!flag") {
				t.Fatalf("%s %v", src, err)
			}
			found = true
		}
	}
	if !found {
		t.Fatal("missing boolean not")
	}
}
func TestLimitsIfAndExpressionDepthIndependently(t *testing.T) {
	p := spec.Problem{ReturnType: "int"}
	h := hole("stmt", 0)
	h.ifDepth = 2
	if len(choices(h, p, 4)) != 1 {
		t.Fatal("if nesting limit")
	}
	h.ifDepth = 1
	branches := choices(h, p, 4)
	if len(branches) != 2 || branches[1].children[0].depth != 0 || branches[1].children[1].ifDepth != 2 {
		t.Fatal("depths mixed")
	}
}
func TestTerminalFailureStillExpandsIncomplete(t *testing.T) {
	p := spec.Problem{Intent: "sum", Name: "Add", Params: []spec.Param{{Name: "x", Type: "int"}, {Name: "y", Type: "int"}}, ReturnType: "int", PublicTests: []spec.Test{{Args: []any{1, 2}, Want: 3}}}
	r, err := Search(context.Background(), p, rank.Fake{}, Options{Beam: 3, MaxCandidates: 100, MaxCalls: 30}, func(body string) (bool, error) { return body == "return (x + y)", nil })
	if err != nil || r.Code != "return (x + y)" {
		t.Fatalf("%+v %v", r, err)
	}
}

type poolingRanker struct{ pooled bool }

func (r *poolingRanker) Rank(_ context.Context, _ string, cs []rank.Candidate) ([]rank.Score, error) {
	a, b := false, false
	out := make([]rank.Score, len(cs))
	for i, c := range cs {
		if strings.HasPrefix(c.Code, "if ") {
			a = true
		}
		if strings.HasPrefix(c.Code, "return ") {
			b = true
		}
		v := float64(len(cs) - i)
		if a && strings.HasPrefix(c.Code, "if ") {
			v += 100
		}
		out[i] = rank.Score{ID: c.ID, Score: v}
	}
	if a && b {
		r.pooled = true
	}
	return out, nil
}
func TestBeamJointlyRanksParents(t *testing.T) {
	p := spec.Problem{Intent: "x", Name: "F", Params: []spec.Param{{Name: "x", Type: "int"}}, ReturnType: "int", Constants: []int{0}, PublicTests: []spec.Test{{Args: []any{1}, Want: 1}}}
	r := &poolingRanker{}
	_, _ = Search(context.Background(), p, r, Options{Beam: 3, MaxCandidates: 80, MaxCalls: 10}, func(string) (bool, error) { return false, nil })
	if !r.pooled {
		t.Fatal("no joint ranking across parents")
	}
}
func TestCloneIndependent(t *testing.T) {
	root := &node{kind: "return", children: []*node{hole("int", 0)}}
	a := clone(root)
	*find(a) = node{kind: "atom", text: "1"}
	if find(root) == nil {
		t.Fatal("clone mutated parent")
	}
}
func TestSearchBudgetsAndCancellation(t *testing.T) {
	p := spec.Problem{Intent: "x", Name: "F", ReturnType: "int", Constants: []int{0}, PublicTests: []spec.Test{{Want: 0}}}
	_, err := Search(context.Background(), p, rank.Fake{}, Options{Beam: 3, MaxCandidates: 1}, func(string) (bool, error) { return false, nil })
	if !errors.Is(err, ErrBudget) {
		t.Fatalf("candidate budget: %v", err)
	}
	_, err = Search(context.Background(), p, rank.Fake{}, Options{Beam: 3, MaxCalls: 1}, func(string) (bool, error) { return false, nil })
	if !errors.Is(err, ErrBudget) {
		t.Fatalf("call budget: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = Search(ctx, p, rank.Fake{}, Options{Beam: 3}, func(string) (bool, error) { return false, nil })
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel: %v", err)
	}
}

func TestStrictGreedyDoesNotTrySecondTerminalCandidate(t *testing.T) {
	p := spec.Problem{Intent: "constant one", Name: "F", Params: []spec.Param{{Name: "x", Type: "int"}}, ReturnType: "int", Constants: []int{1}, PublicTests: []spec.Test{{Args: []any{2}, Want: 1}}}
	for _, strict := range []bool{false, true} {
		checks := 0
		result, err := Search(context.Background(), p, rank.Fake{}, Options{Beam: 1, Strict: strict, MaxCandidates: 100}, func(body string) (bool, error) {
			checks++
			return body == "return 1", nil
		})
		if strict {
			if !errors.Is(err, ErrNoSolution) || checks != 1 {
				t.Fatalf("strict checked %d terminals: %v", checks, err)
			}
		} else if err != nil || result.Code != "return 1" || checks != 2 {
			t.Fatalf("fallback: checks=%d error=%v code=%q", checks, err, result.Code)
		}
	}
}

func TestCloneDoesNotShareLocalBindings(t *testing.T) {
	root := stmtHole([]localSymbol{{id: 0, name: "v0", typ: "int"}}, 0, false)
	branch := clone(root)
	branch.scope[0].typ = "bool"
	if root.scope[0].typ != "int" {
		t.Fatal("branch changed its parent's binding")
	}
}
