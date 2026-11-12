package researchdata

import (
	"jevcancode/internal/tokenspec"
	"strings"
	"testing"
)

func TestFrontierReportsFirstBlockedLiteral(t *testing.T) {
	p, err := tokenspec.Decode(strings.NewReader(`{"intent":"number","inputs":[],"allowed_types":["int"],"literals":{"integers":[0,1]},"public_tests":[{"inputs":{},"want":9}],"return_policy":"values-only-v1"}`))
	if err != nil {
		t.Fatal(err)
	}
	report, err := CheckFrontier(p, "package main\nfunc f0() int { return 9 }")
	if err != nil {
		t.Fatal(err)
	}
	if report.Reachable || report.Blocked != "9" || !strings.Contains(report.Prefix, "return") {
		t.Fatalf("unexpected frontier: %+v", report)
	}
}
