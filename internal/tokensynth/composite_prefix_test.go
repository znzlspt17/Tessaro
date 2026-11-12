package tokensynth

import (
	"tessaro/internal/tokenspec"
	"testing"
)

func TestCompositeLiteralBraceRequiresType(t *testing.T) {
	p := contractFixture(t, []string{"flag"}, []string{"bool"}, "bool")
	p.ReturnPolicy = tokenspec.ReturnPolicyValuesOnly
	for _, src := range []string{
		"package main ; func f0 ( flag bool ) bool { { ! flag",
		"package main ; func f0 ( flag bool ) bool { { true",
		"package main ; func f0 ( flag bool ) bool { v0 := int",
	} {
		s := semanticState(t, p, src)
		if hasSemanticChoice(t, p, s, "{") {
			t.Errorf("invalid literal brace offered after %s", src)
		}
	}
	for _, src := range []string{
		"package main ; func f0 ( flag bool ) bool { if flag",
		"package main ; func f0 ( flag bool ) bool { if ! flag",
		"package main ; func f0 ( flag bool ) bool { for flag",
	} {
		s := semanticState(t, p, src)
		if !hasSemanticChoice(t, p, s, "{") {
			t.Errorf("valid block brace missing after %s", src)
		}
	}
}
func TestCompositeLiteralBraceKeepsCollections(t *testing.T) {
	p := contractFixture(t, []string{"n"}, []string{"int"}, "int")
	p.ReturnPolicy = tokenspec.ReturnPolicyValuesOnly
	for _, src := range []string{"package main ; func f0 ( n int ) int { v0 := [ ] int", "package main ; func f0 ( n int ) int { v0 := map [ string ] int"} {
		s := semanticState(t, p, src)
		if !hasSemanticChoice(t, p, s, "{") {
			t.Errorf("collection brace missing after %s", src)
		}
	}
}
