package researchdata

import (
	"jevcancode/internal/tokenspec"
	"jevcancode/internal/tokenverify"
	"strings"
	"testing"
)

func TestVariantRewritesComputedReturnsWithoutCollision(t *testing.T) {
	p, err := tokenspec.Decode(strings.NewReader(`{"intent":"sum","inputs":["a"],"allowed_types":["int"],"public_tests":[{"inputs":{"a":2},"want":3}],"return_policy":"values-only-v1"}`))
	if err != nil {
		t.Fatal(err)
	}
	src := `package main
func f0(a int) int { v0 := a; if v0 > 0 { return v0 + 1 }; return -1 }`
	variant, err := Variant(src)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tokenverify.Validate(p, variant); err != nil {
		t.Fatalf("variant rejected: %v\n%s", err, variant)
	}
	if !strings.Contains(variant, "v1 := v0 + 1") || !strings.Contains(variant, "return -1") {
		t.Fatalf("unexpected variant:\n%s", variant)
	}
}
