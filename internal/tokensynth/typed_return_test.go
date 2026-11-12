package tokensynth

import (
	"context"
	"tessaro/internal/tokenspec"
	"tessaro/internal/tokenverify"
	"strings"
	"testing"
)

func TestTypedReturnCandidatesBeforeRank(t *testing.T) {
	p := contractFixture(t, []string{"n"}, []string{"int"}, "int")
	p.ReturnPolicy = tokenspec.ReturnPolicyValuesOnly
	p.Literals.Integers = []int{0, 1}
	s := semanticState(t, p, "package main ; func f0 ( n int ) int { return")
	for _, v := range []string{"nil", "true", "false", "len", "int"} {
		if hasSemanticChoice(t, p, s, v) {
			t.Errorf("unexpected %q", v)
		}
	}
	for _, v := range []string{"n", "1", "-", "("} {
		if !hasSemanticChoice(t, p, s, v) {
			t.Errorf("missing %q", v)
		}
	}
	r := &returnChoiceRecorder{}
	_, _ = searchFrom(context.Background(), p, r, Options{Strategy: "greedy", PublicEvaluate: func(context.Context, string) (tokenverify.PublicEvaluation, error) {
		return tokenverify.PublicEvaluation{}, nil
	}}, func(string) (bool, error) { return false, nil }, []*State{s})
	if len(r.seen) == 0 {
		t.Fatal("ranker not called")
	}
	for _, v := range r.seen {
		switch v {
		case "nil", "true", "false", "len", "int":
			t.Errorf("ranker saw %q", v)
		}
	}
	p.ReturnPolicy = ""
	if !hasSemanticChoice(t, p, s, "true") {
		t.Fatal("legacy filter changed")
	}
}
func TestTypedReturnBoolAndNil(t *testing.T) {
	p := contractFixture(t, []string{"flag"}, []string{"bool"}, "bool")
	p.ReturnPolicy = tokenspec.ReturnPolicyValuesOnly
	p.Literals.Integers = []int{0, 1}
	s := semanticState(t, p, "package main ; func f0 ( flag bool ) bool { return (")
	for _, v := range []string{"true", "flag", "("} {
		if !hasSemanticChoice(t, p, s, v) {
			t.Errorf("missing %s", v)
		}
	}
	for _, v := range []string{"nil", "1", "-"} {
		if hasSemanticChoice(t, p, s, v) {
			t.Errorf("unexpected %s", v)
		}
	}
	q := contractFixture(t, []string{"xs"}, []string{"[]int"}, "[]int")
	q.ReturnPolicy = tokenspec.ReturnPolicyValuesOnly
	a := semanticState(t, q, "package main ; func f0 ( xs [ ] int ) [ ] int { return")
	if !hasSemanticChoice(t, q, a, "nil") || !hasSemanticChoice(t, q, a, "xs") {
		t.Fatal("slice values missing")
	}
}
func TestTypedReturnInputBuiltinShadow(t *testing.T) {
	p := contractFixture(t, []string{"n"}, []string{"int"}, "int")
	p.ReturnPolicy = tokenspec.ReturnPolicyValuesOnly
	s, err := NewState()
	if err != nil {
		t.Fatal(err)
	}
	for _, lex := range strings.Fields("package main ; func f0 ( n int ) int { len := n ;") {
		s, err = s.Advance(Choice{Lexeme: lex}, "shadow")
		if err != nil {
			t.Fatal(err)
		}
	}
	s.semantic = assessSemantic(p, s.Tokens, nil)
	for _, c := range mustNext(t, p, s) {
		if c.Lexeme == "return" {
			s, err = s.Advance(c, "return")
			break
		}
	}
	if err != nil {
		t.Fatal(err)
	}
	if !hasSemanticChoice(t, p, s, "len") {
		t.Fatal("shadowed builtin variable missing")
	}
}

func TestTypedReturnUnknownScopeAndContractKey(t *testing.T) {
	p := contractFixture(t, []string{"n"}, []string{"int"}, "int")
	p.ReturnPolicy = tokenspec.ReturnPolicyValuesOnly
	s := semanticState(t, p, "package main ; func f0 ( n int ) int { v0 := true ; return")
	s.semantic = nil
	if !hasSemanticChoice(t, p, s, "v0") {
		t.Fatal("unknown variable type must remain")
	}
	q := contractFixture(t, []string{"n"}, []string{"int"}, "bool")
	q.ReturnPolicy = tokenspec.ReturnPolicyValuesOnly
	if semanticKey(p) == semanticKey(q) {
		t.Fatal("inferred result type missing from key")
	}
}
func TestTypedReturnMapNilAndNegativeParenthesized(t *testing.T) {
	p := contractFixture(t, []string{"m"}, []string{"map[string]int"}, "map[string]int")
	p.ReturnPolicy = tokenspec.ReturnPolicyValuesOnly
	s := semanticState(t, p, "package main ; func f0 ( m map [ string ] int ) map [ string ] int { return")
	if !hasSemanticChoice(t, p, s, "nil") || !hasSemanticChoice(t, p, s, "m") {
		t.Fatal("map values missing")
	}
	q := contractFixture(t, []string{"n"}, []string{"int"}, "int")
	q.ReturnPolicy = tokenspec.ReturnPolicyValuesOnly
	q.Literals.Integers = []int{0, 1}
	v := semanticState(t, q, "package main ; func f0 ( n int ) int { return ( -")
	if !hasSemanticChoice(t, q, v, "1") {
		t.Fatal("negative literal missing")
	}
}
