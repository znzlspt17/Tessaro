package tokensynth

import (
	"tessaro/internal/tokenspec"
	"testing"
)

func TestVisibleTypesHeaderAndScopes(t *testing.T) {
	p := contractFixture(t, []string{"n"}, []string{"int"}, "int")
	p.ReturnPolicy = tokenspec.ReturnPolicyValuesOnly
	h := semanticState(t, p, "package main ; func f0 ( n")
	if _, ok := VisibleTypes(p, h)["n"]; ok {
		t.Fatal("uncommitted parameter exposed")
	}
	s := semanticState(t, p, "package main ; func f0 ( n int ) int { v0 := n ; { v0 := true ;")
	if got := VisibleTypes(p, s); got["n"] != "int" || got["v0"] != "bool" {
		t.Fatalf("inner scope: %v", got)
	}
	s = semanticState(t, p, "package main ; func f0 ( n int ) int { v0 := n ; { v0 := true ; } ;")
	if got := VisibleTypes(p, s); got["v0"] != "int" {
		t.Fatalf("outer scope: %v", got)
	}
}
func TestVisibleTypesUnknownAndLegacy(t *testing.T) {
	p := contractFixture(t, []string{"n"}, []string{"int"}, "int")
	p.ReturnPolicy = tokenspec.ReturnPolicyValuesOnly
	s := semanticState(t, p, "package main ; func f0 ( n int ) int { v0 := n ;")
	s.semantic = nil
	if _, ok := VisibleTypes(p, s)["v0"]; ok {
		t.Fatal("unknown local type exposed")
	}
	p.ReturnPolicy = ""
	s = semanticState(t, p, "package main ; func f0 ( n int ) int {")
	if got := VisibleTypes(p, s); got["n"] != "int" {
		t.Fatalf("legacy input: %v", got)
	}
}
