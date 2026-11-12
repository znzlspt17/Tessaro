package hypothesis

import (
	"context"
	"errors"
	"tessaro/internal/rank"
)

var errRequestBudget = errors.New("request_budget_exhausted")
var errModelMismatch = errors.New("model_mismatch")
var errService = errors.New("service_failure")

type guardedRanker struct {
	inner                       rank.Ranker
	used, limit                 int
	failure                     error
	actualModel                 string
	observe                     func(kind, state string, cs []rank.Candidate, decision rank.Decision)
	inputTokens, outputTokens   int
	inputUnknown, outputUnknown bool
}

// 2026-11-08: 실패와 모델 불일치를 고정해 재시도나 한도 초과 호출을 방지한다.
func (g *guardedRanker) RankDecision(ctx context.Context, state string, cs []rank.Candidate) (rank.Decision, error) {
	if e := ctx.Err(); e != nil {
		return rank.Decision{}, e
	}
	if g.failure != nil {
		return rank.Decision{}, g.failure
	}
	if g.used >= g.limit {
		g.failure = errRequestBudget
		return rank.Decision{}, g.failure
	}
	dr, ok := g.inner.(rank.DecisionRanker)
	if !ok {
		g.failure = errService
		return rank.Decision{}, g.failure
	}
	g.used++
	if g.observe != nil {
		g.observe("request", state, cs, rank.Decision{})
	}
	d, e := dr.RankDecision(ctx, state, cs)
	if d.Usage == nil || d.Usage.InputTokens == nil {
		g.inputUnknown = true
	} else {
		g.inputTokens += *d.Usage.InputTokens
	}
	if d.Usage == nil || d.Usage.OutputTokens == nil {
		g.outputUnknown = true
	} else {
		g.outputTokens += *d.Usage.OutputTokens
	}
	if e != nil {
		if ctx.Err() != nil {
			return rank.Decision{}, ctx.Err()
		}
		g.failure = errService
		return rank.Decision{}, g.failure
	}
	provider, ok := g.inner.(rank.MetadataProvider)
	if !ok {
		g.failure = errModelMismatch
		return rank.Decision{}, g.failure
	}
	metadata := provider.Metadata()
	g.actualModel = metadata.ReturnedModel
	if g.actualModel != "jev-1.14.0" {
		g.failure = errModelMismatch
		return rank.Decision{}, g.failure
	}
	if rank.Validate(cs, d.Scores) != nil || d.ChosenID == "" {
		g.failure = errService
		return rank.Decision{}, g.failure
	}
	found := false
	for _, c := range cs {
		if c.ID == d.ChosenID {
			found = true
		}
	}
	if !found {
		g.failure = errService
		return rank.Decision{}, g.failure
	}
	if g.observe != nil {
		g.observe("decision", state, cs, d)
	}
	return d, nil
}
func (g *guardedRanker) Rank(ctx context.Context, state string, cs []rank.Candidate) ([]rank.Score, error) {
	d, e := g.RankDecision(ctx, state, cs)
	return d.Scores, e
}
func (g *guardedRanker) Metadata() rank.Metadata {
	return rank.Metadata{Model: "jev-1.14.0", ReturnedModel: g.actualModel}
}
func (g *guardedRanker) usage() *rank.Usage {
	if g.used == 0 {
		return nil
	}
	u := &rank.Usage{}
	if !g.inputUnknown {
		v := g.inputTokens
		u.InputTokens = &v
	}
	if !g.outputUnknown {
		v := g.outputTokens
		u.OutputTokens = &v
	}
	return u
}
