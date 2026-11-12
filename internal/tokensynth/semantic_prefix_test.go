package tokensynth

import (
	"tessaro/internal/tokenspec"
	"strings"
	"testing"
)

func semanticState(t *testing.T, p tokenspec.Problem, source string) *State {
	t.Helper()
	s, err := NewState()
	if err != nil {
		t.Fatal(err)
	}
	for _, lex := range strings.Fields(source) {
		found := false
		for _, c := range mustNext(t, p, s) {
			if c.Lexeme == lex {
				s, err = s.Advance(c, "test")
				if err != nil {
					t.Fatal(err)
				}
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("%q missing after %q", lex, s.Source())
		}
	}
	return s
}
func hasSemanticChoice(t *testing.T, p tokenspec.Problem, s *State, lex string) bool {
	t.Helper()
	for _, c := range mustNext(t, p, s) {
		if c.Lexeme == lex {
			return true
		}
	}
	return false
}
func TestSemanticPrefixRejectsCommittedMismatch(t *testing.T) {
	p := contractFixture(t, []string{"n"}, []string{"int"}, "int")
	p.ReturnPolicy = tokenspec.ReturnPolicyValuesOnly
	p.Literals.Integers = []int{0, 1}
	s := semanticState(t, p, "package main ; func f0 ( n int ) int { v0 := true ; v0 = 1")
	if hasSemanticChoice(t, p, s, ";") {
		t.Fatal("committed bool = int should be rejected")
	}
	p.ReturnPolicy = ""
	if !hasSemanticChoice(t, p, s, ";") {
		t.Fatal("legacy should remain unchanged")
	}
}
func TestSemanticPrefixPreservesUnfinishedAndValid(t *testing.T) {
	p := contractFixture(t, []string{"n"}, []string{"int"}, "int")
	p.ReturnPolicy = tokenspec.ReturnPolicyValuesOnly
	p.Literals.Integers = []int{0, 1}
	for _, src := range []string{"package main ; func f0 ( n int ) int { v0 := true ; v0 = false", "package main ; func f0 ( n int ) int { v0 := 1 ; v0 = n"} {
		s := semanticState(t, p, src)
		if !hasSemanticChoice(t, p, s, ";") {
			t.Fatalf("valid statement rejected: %s", src)
		}
	}
	s := semanticState(t, p, "package main ; func f0 ( n int ) int { if n")
	if !hasSemanticChoice(t, p, s, ">") {
		t.Fatal("unfinished condition must remain")
	}
}

func TestSemanticCommittedConditionAndExpression(t *testing.T) {
	p := contractFixture(t, []string{"n"}, []string{"int"}, "int")
	p.ReturnPolicy = tokenspec.ReturnPolicyValuesOnly
	p.Literals.Integers = []int{0, 1}
	for _, tc := range []struct{ source, choice string }{
		{"package main ; func f0 ( n int ) int { v0 := n + true", ";"},
		{"package main ; func f0 ( n int ) int { if n", "{"},
	} {
		s := semanticState(t, p, tc.source)
		if hasSemanticChoice(t, p, s, tc.choice) {
			t.Fatalf("invalid committed prefix accepted: %s %s", tc.source, tc.choice)
		}
	}
	s := semanticState(t, p, "package main ; func f0 ( n int ) int { if n > 0")
	if !hasSemanticChoice(t, p, s, "{") {
		t.Fatal("valid condition rejected")
	}
}

func TestSemanticScopesAndUnfinishedCollections(t *testing.T) {
	p := contractFixture(t, []string{"n", "xs"}, []string{"int", "[]int"}, "int")
	p.ReturnPolicy = tokenspec.ReturnPolicyValuesOnly
	p.Literals.Integers = []int{0, 1}
	for _, src := range []string{
		"package main ; func f0 ( n int , xs [ ] int ) int { v0 := n ; { v0 := true ; v0 = false ; } ; v0 = n ; return - n ; }",
		"package main ; func f0 ( n int , xs [ ] int ) int { v0 := append ( xs , n ) ; v1 := len ( v0 ) ; return v1 ; }",
		"package main ; func f0 ( n int , xs [ ] int ) int { for v0 := 0 ; v0 < n ; v0 ++ { n = n + 1 ; } ; return n ; }",
	} {
		s := semanticState(t, p, src)
		if !Complete(s) {
			t.Fatalf("incomplete: %s", src)
		}
	}
	s := semanticState(t, p, "package main ; func f0 ( n int , xs [ ] int ) int { v0 := append ( xs ,")
	if !hasSemanticChoice(t, p, s, "n") {
		t.Fatal("unfinished call rejected")
	}
}

func TestSemanticBranchesRemainIndependent(t *testing.T) {
	p := contractFixture(t, []string{"n"}, []string{"int"}, "int")
	p.ReturnPolicy = tokenspec.ReturnPolicyValuesOnly
	p.Literals.Integers = []int{0, 1}
	s := semanticState(t, p, "package main ; func f0 ( n int ) int { v0 := true ; v0 =")
	var good, bad *State
	for _, c := range mustNext(t, p, s) {
		if c.Lexeme == "true" {
			good, _ = s.Advance(c, "good")
		}
		if c.Lexeme == "1" {
			bad, _ = s.Advance(c, "bad")
		}
	}
	if good == nil || bad == nil || !hasSemanticChoice(t, p, good, ";") || hasSemanticChoice(t, p, bad, ";") || !hasSemanticChoice(t, p, good, ";") {
		t.Fatal("branches interfere")
	}
}

func TestSemanticScopeSnapshotReusedAndRestored(t *testing.T) {
	p := contractFixture(t, []string{"n"}, []string{"int"}, "int")
	p.ReturnPolicy = tokenspec.ReturnPolicyValuesOnly
	p.Literals.Integers = []int{0, 1}
	s := semanticState(t, p, "package main ; func f0 ( n int ) int { v0 := n ; { v0 := true ;")
	if s.semantic == nil || s.semantic.variables["v0"] == nil || s.semantic.variables["v0"].String() != "bool" {
		t.Fatalf("inner scope snapshot: %+v", s.semantic)
	}
	var child *State
	for _, c := range mustNext(t, p, s) {
		if c.Lexeme == "v0" {
			child, _ = s.Advance(c, "child")
			break
		}
	}
	if child == nil || child.semantic != s.semantic {
		t.Fatal("unchanged context should reuse snapshot")
	}
	s = semanticState(t, p, "package main ; func f0 ( n int ) int { v0 := n ; { v0 := true ; } ;")
	if s.semantic == nil || s.semantic.variables["v0"] == nil || s.semantic.variables["v0"].String() != "int" {
		t.Fatalf("outer scope snapshot: %+v", s.semantic)
	}
}

func TestSemanticCommittedInvalidIndexAndCall(t *testing.T) {
	p := contractFixture(t, []string{"n"}, []string{"int"}, "int")
	p.ReturnPolicy = tokenspec.ReturnPolicyValuesOnly
	p.Literals.Integers = []int{0, 1}
	for _, src := range []string{"package main ; func f0 ( n int ) int { v0 := n [ 0 ]", "package main ; func f0 ( n int ) int { v0 := n ( 1 )"} {
		s := semanticState(t, p, src)
		if hasSemanticChoice(t, p, s, ";") {
			t.Fatalf("invalid expression accepted: %s", src)
		}
	}
}
