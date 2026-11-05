package main

import (
	"jevcancoding/internal/tokenspec"
	"time"
)

type limits struct {
	MaxTokens, MaxCandidates, MaxCalls, MaxDepth, MaxLocals, MaxStatements, MaxNesting int
	Timeout                                                                            time.Duration
}

func defaultLimits(engine string) limits {
	if engine == "token" {
		return limits{MaxTokens: 256, MaxCandidates: 8192, MaxCalls: 128, MaxDepth: 4, MaxLocals: 8, MaxStatements: 32, MaxNesting: 4, Timeout: 300 * time.Second}
	}
	return limits{MaxCandidates: 2000, MaxCalls: 200, MaxDepth: 4, MaxLocals: 2, MaxStatements: 8, Timeout: 300 * time.Second}
}
func tokenLimits(out limits, b tokenspec.Budgets, flags limits, seen map[string]bool) limits {
	if b.MaxTokens > 0 {
		out.MaxTokens = b.MaxTokens
	}
	if b.MaxCandidates > 0 {
		out.MaxCandidates = b.MaxCandidates
	}
	if b.MaxCalls > 0 {
		out.MaxCalls = b.MaxCalls
	}
	if b.MaxLocals > 0 {
		out.MaxLocals = b.MaxLocals
	}
	if b.MaxStatements > 0 {
		out.MaxStatements = b.MaxStatements
	}
	if b.MaxNesting > 0 {
		out.MaxNesting = b.MaxNesting
	}
	if b.TimeoutSeconds > 0 {
		out.Timeout = time.Duration(b.TimeoutSeconds) * time.Second
	}
	if seen["max-tokens"] {
		out.MaxTokens = flags.MaxTokens
	}
	if seen["max-candidates"] {
		out.MaxCandidates = flags.MaxCandidates
	}
	if seen["max-calls"] {
		out.MaxCalls = flags.MaxCalls
	}
	if seen["max-locals"] {
		out.MaxLocals = flags.MaxLocals
	}
	if seen["max-statements"] {
		out.MaxStatements = flags.MaxStatements
	}
	if seen["max-nesting"] {
		out.MaxNesting = flags.MaxNesting
	}
	if seen["timeout"] {
		out.Timeout = flags.Timeout
	}
	return out
}
