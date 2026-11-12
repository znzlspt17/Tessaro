package tokensynth

import (
	"context"
	"jevcancode/internal/rank"
	"jevcancode/internal/tokenspec"

	"testing"
)

func TestStandaloneNotRejectedBeforeRank(t *testing.T) {
	p := contractFixture(t, []string{"flag"}, []string{"bool"}, "bool")
	p.ReturnPolicy = tokenspec.ReturnPolicyValuesOnly
	s := semanticState(t, p, "package main ; func f0 ( flag bool ) bool {")
	seen := false
	called := false
	_, _ = searchFrom(context.Background(), p, candidateRecorder{fn: func(cs []rank.Candidate) {
		called = true
		for _, c := range cs {
			if c.Lexeme == "!" {
				seen = true
			}
		}
	}}, Options{Strategy: "beam"}, func(string) (bool, error) { return false, nil }, []*State{s})
	if !called || seen {
		t.Fatalf("called=%v saw !=%v", called, seen)
	}
}
func TestUnaryContextsRemain(t *testing.T) {
	p := contractFixture(t, []string{"flag"}, []string{"bool"}, "bool")
	p.ReturnPolicy = tokenspec.ReturnPolicyValuesOnly
	for _, src := range []string{"package main ; func f0 ( flag bool ) bool { if", "package main ; func f0 ( flag bool ) bool { for", "package main ; func f0 ( flag bool ) bool { v0 :=", "package main ; func f0 ( flag bool ) bool { v0 := len (", "package main ; func f0 ( flag bool ) bool { ("} {
		s := semanticState(t, p, src)
		if s.irrecoverableExpression("!") {
			t.Errorf("valid/uncertain context pruned: %s", src)
		}
	}
}
func TestStandaloneNotInitialStateRejected(t *testing.T) {
	p := contractFixture(t, []string{"flag"}, []string{"bool"}, "bool")
	p.ReturnPolicy = tokenspec.ReturnPolicyValuesOnly
	s := semanticState(t, p, "package main ; func f0 ( flag bool ) bool { ! flag")
	if !s.irrecoverableExpression("") {
		t.Fatal("standalone unary not rejected")
	}

}

func TestUnaryValidProgramsThroughCandidates(t *testing.T) {
	p := contractFixture(t, []string{"flag"}, []string{"bool"}, "bool")
	p.ReturnPolicy = tokenspec.ReturnPolicyValuesOnly
	for _, src := range []string{
		"package main ; func f0 ( flag bool ) bool { if ! flag { return true ; } ; return flag ; }",
		"package main ; func f0 ( flag bool ) bool { for ! flag { break ; } ; return flag ; }",
		"package main ; func f0 ( flag bool ) bool { v0 := ! flag ; return v0 ; }",
	} {
		s := semanticState(t, p, src)
		if !Complete(s) {
			t.Fatalf("incomplete: %s", src)
		}
	}
	q := contractFixture(t, []string{"n"}, []string{"int"}, "int")
	q.ReturnPolicy = tokenspec.ReturnPolicyValuesOnly
	q.Literals.Integers = []int{1}
	s := semanticState(t, q, "package main ; func f0 ( n int ) int { return ( - 1 ) ; }")
	if !Complete(s) {
		t.Fatal("negative return missing")
	}
}
