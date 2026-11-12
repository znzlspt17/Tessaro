package main

import (
	"jevcancode/internal/tokenspec"
	"testing"
)

func TestTokenDefaultsAndExplicitBudgetPrecedence(t *testing.T) {
	defaults := defaultLimits("token")
	if defaults.MaxTokens != 256 || defaults.MaxCalls != 128 || defaults.MaxCandidates != 8192 || defaults.MaxLocals != 8 || defaults.MaxStatements != 32 || defaults.MaxNesting != 4 {
		t.Fatalf("%+v", defaults)
	}
	limits := tokenLimits(defaults, tokenspec.Budgets{MaxCalls: 7, MaxLocals: 3}, limits{MaxCalls: 5, MaxLocals: 0}, map[string]bool{"max-calls": true, "max-locals": true})
	if limits.MaxCalls != 5 || limits.MaxLocals != 0 {
		t.Fatalf("%+v", limits)
	}
}
