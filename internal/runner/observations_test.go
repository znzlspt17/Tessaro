package runner

import (
	"context"
	"encoding/json"
	"errors"
	"jevcancoding/internal/rank"
	"jevcancoding/internal/spec"
	"jevcancoding/internal/synth"
	"math"
	"strings"
	"testing"
)

type usageModel struct {
	calls int
	usage *rank.Usage
	err   error
}

func (m *usageModel) Rank(context.Context, string, []rank.Candidate) ([]rank.Score, error) {
	panic("optional usage must not issue a second request")
}
func (m *usageModel) RankWithUsage(ctx context.Context, intent string, candidates []rank.Candidate) ([]rank.Score, *rank.Usage, error) {
	m.calls++
	scores, _ := (rank.Fake{}).Rank(ctx, intent, candidates)
	return scores, m.usage, m.err
}

func observationProblem() spec.Problem {
	return spec.Problem{Intent: "private task context", Name: "F", Params: []spec.Param{{Name: "x", Type: "int"}}, ReturnType: "int", PublicTests: []spec.Test{{Args: []any{1}, Want: 1}}}
}

func TestUsageAndCostDoNotInventMissingValues(t *testing.T) {
	input, output := 12, 3
	inputPrice, outputPrice := 2.0, 3.0
	for _, tc := range []struct {
		name  string
		usage *rank.Usage
		known bool
	}{
		{"known", &rank.Usage{InputTokens: &input, OutputTokens: &output}, true},
		{"absent", nil, false},
		{"partial", &rank.Usage{InputTokens: &input}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			model := &usageModel{usage: tc.usage}
			result := Run(context.Background(), observationProblem(), model, Request{Strategy: "greedy", MaxCalls: 1, InputPrice: &inputPrice, OutputPrice: &outputPrice})
			if model.calls != 1 || result.Calls != 1 || result.Status != "budget_exhausted" {
				t.Fatalf("unexpected execution: %+v calls=%d", result, model.calls)
			}
			if tc.known {
				if result.EstimatedCost == nil || math.Abs(*result.EstimatedCost-0.000033) > 1e-12 {
					t.Fatalf("cost=%v", result.EstimatedCost)
				}
			} else if result.EstimatedCost != nil {
				t.Fatalf("unknown usage assigned a cost: %v", result.EstimatedCost)
			}
		})
	}
}

func TestModelErrorIsClassifiedWithoutDiagnosticLeak(t *testing.T) {
	marker := "untrusted diagnostic must never be persisted"
	model := &usageModel{err: errors.New(marker)}
	result := Run(context.Background(), observationProblem(), model, Request{Strategy: "greedy"})
	data, err := json.Marshal(result)
	if err != nil || strings.Contains(string(data), marker) || result.Status != "model_error" {
		t.Fatalf("status=%s marshal=%v", result.Status, err)
	}
}

func TestInvalidPriceFailsBeforeCallingModel(t *testing.T) {
	for _, price := range []float64{-1, math.NaN(), math.Inf(1)} {
		model := &usageModel{}
		result := Run(context.Background(), observationProblem(), model, Request{Strategy: "greedy", InputPrice: &price})
		if result.Status != "input_error" || model.calls != 0 {
			t.Fatalf("price=%v status=%s calls=%d", price, result.Status, model.calls)
		}
	}
}

func TestRequestOverlayDoesNotMutateProblem(t *testing.T) {
	p := observationProblem()
	locals, calls := 2, 1
	p.Budgets = &spec.Budgets{MaxLocals: &locals, MaxCalls: &calls}
	model := &usageModel{}
	result := Run(context.Background(), p, model, Request{Strategy: "greedy", LocalLimitSet: true, MaxLocals: 0})
	if *p.Budgets.MaxLocals != 2 || result.EffectiveBudgets["max_locals"] != 0 || model.calls != 1 {
		t.Fatalf("problem mutated or budget lost: %+v", result)
	}
}

func TestTraceRecordsSafeTerminationWithoutContextOrDiagnostic(t *testing.T) {
	marker := "untrusted diagnostic must never be persisted"
	model := &usageModel{err: errors.New(marker)}
	var events []synth.TraceEvent
	result := Run(context.Background(), observationProblem(), model, Request{Strategy: "greedy", Trace: func(e synth.TraceEvent) { events = append(events, e) }})
	found := false
	for _, event := range events {
		if event.Reason == "ended:"+result.Status {
			found = true
		}
		if event.Code != "" {
			t.Fatal("default trace includes code")
		}
	}
	data, _ := json.Marshal(events)
	if !found || strings.Contains(string(data), marker) || strings.Contains(string(data), observationProblem().Intent) {
		t.Fatalf("unsafe or incomplete trace: %s", data)
	}
}
