package tokensynth

import (
	"context"
	"fmt"
	"jevcancode/internal/rank"
	"jevcancode/internal/tokenspec"
	"sort"
	"strconv"
	"strings"
	"testing"
)

func TestResearchIDsRemainBoundedAtDeepPrefix(t *testing.T) {
	p := subtractionSpec(t)
	s := advanceContract(t, p, strings.Fields("package main ; func f0 ( a int , b int ) int { return"))
	var err error
	for i := 0; i < 52; i++ {
		s, err = s.Advance(Choice{Lexeme: "("}, fmt.Sprintf("%s:0", s.ID))
		if err != nil {
			t.Fatal(err)
		}
	}
	if len(s.Tokens) <= 50 {
		t.Fatalf("prefix not deep: %d", len(s.Tokens))
	}
	var candidates []rank.Candidate
	_, _ = searchFrom(context.Background(), p, candidateRecorder{fn: func(cs []rank.Candidate) { candidates = append([]rank.Candidate(nil), cs...) }}, Options{Strategy: "lds", MaxTokens: len(s.Tokens) + 1, MaxCalls: 1}, func(string) (bool, error) { return false, nil }, []*State{s})
	if len(candidates) < 2 {
		t.Fatalf("no model candidates at depth %d", len(s.Tokens))
	}
	ids := map[string]bool{}
	for _, c := range candidates {
		if len(c.ID) > 17 || ids[c.ID] || c.ParentID != s.ID {
			t.Fatalf("not bounded, unique, and parent-linked: %+v", c)
		}
		ids[c.ID] = true
	}
}

type flatRanker struct{}

func (flatRanker) Rank(_ context.Context, _ string, cs []rank.Candidate) ([]rank.Score, error) {
	s := make([]rank.Score, len(cs))
	for i, c := range cs {
		s[i] = rank.Score{ID: c.ID, Score: 1}
	}
	return s, nil
}
func TestOpaqueIDsPreserveEqualScoreLegacyOrdering(t *testing.T) {
	p := subtractionSpec(t)
	s := advanceContract(t, p, strings.Fields("package main ; func f0 ( a int , b int ) int { return"))
	var available, ranked []string
	first := 0
	_, _ = searchFrom(context.Background(), p, flatRanker{}, Options{Strategy: "lds", MaxTokens: len(s.Tokens) + 1, MaxCalls: 1, Trace: func(e Event) {
		if e.Reason == "available" {
			if first == 0 {
				first = e.Step
			}
			if e.Step == first {
				available = append(available, e.Lexeme)
			}
		}
		if e.Reason == "ranked" && e.Step == first {
			ranked = append(ranked, e.Lexeme)
		}
	}}, func(string) (bool, error) { return false, nil }, []*State{s})
	if len(available) < 12 || len(ranked) != len(available) {
		t.Fatalf("not enough choices: available=%v ranked=%v", available, ranked)
	}
	order := make([]int, len(available))
	for i := range order {
		order[i] = i
	}
	sort.Slice(order, func(i, j int) bool { return strconv.Itoa(order[i]) < strconv.Itoa(order[j]) })
	for i, index := range order {
		if ranked[i] != available[index] {
			t.Fatalf("equal-score order changed at %d: got %v want index order %v available %v", i, ranked, order, available)
		}
	}
}

type idBatchRanker struct{ batches [][]rank.DecisionRequest }

func (*idBatchRanker) SupportsDecisionBatch() bool { return true }
func (*idBatchRanker) Rank(_ context.Context, _ string, cs []rank.Candidate) ([]rank.Score, error) {
	out := make([]rank.Score, len(cs))
	for i, c := range cs {
		out[i] = rank.Score{ID: c.ID, Score: float64(len(cs) - i)}
	}
	return out, nil
}
func (r *idBatchRanker) RankDecisions(ctx context.Context, requests []rank.DecisionRequest) ([]rank.Decision, *rank.Usage, error) {
	r.batches = append(r.batches, requests)
	decisions := make([]rank.Decision, len(requests))
	for i, q := range requests {
		s, _ := r.Rank(ctx, q.State, q.Candidates)
		decisions[i] = rank.Decision{Scores: s}
	}
	return decisions, nil, nil
}
func TestOpaqueIDsAreUniqueAcrossPrefetchedParents(t *testing.T) {
	p := subtractionSpec(t)
	s := advanceContract(t, p, strings.Fields("package main ; func f0 ( a int , b int ) int { return"))
	other := *s
	other.ID = "other"
	r := &idBatchRanker{}
	_, _ = searchFrom(context.Background(), p, r, Options{Strategy: "lds", DecisionBatchSize: 2, MaxTokens: len(s.Tokens) + 1, MaxCalls: 2}, func(string) (bool, error) { return false, nil }, []*State{s, &other})
	if len(r.batches) != 1 || len(r.batches[0]) != 2 {
		t.Fatalf("missing independent batch: %+v", r.batches)
	}
	seen := map[string]bool{}
	for _, q := range r.batches[0] {
		parent := q.Candidates[0].ParentID
		for _, c := range q.Candidates {
			if c.ParentID != parent || len(c.ID) != 17 || seen[c.ID] {
				t.Fatalf("collision or parent mismatch: %+v", c)
			}
			seen[c.ID] = true
		}
	}
}

func TestModelContextIncludesOnlyConfirmedVisibleTypes(t *testing.T) {
	p := contractFixture(t, []string{"n"}, []string{"int"}, "int")
	p.ReturnPolicy = tokenspec.ReturnPolicyValuesOnly
	state := semanticState(t, p, "package main ; func f0 ( n int ) int {")
	for _, strategy := range []string{"beam", "lds"} {
		t.Run(strategy, func(t *testing.T) {
			recorder := &policyStateRecorder{}
			_, _ = searchFrom(context.Background(), p, recorder, Options{Strategy: strategy}, func(string) (bool, error) { return false, nil }, []*State{state})
			if len(recorder.states) != 1 {
				t.Fatalf("no decision: %+v", recorder.states)
			}
			parents, ok := recorder.states[0]["parents"].(map[string]any)
			if !ok {
				t.Fatal("parents missing")
			}
			parent, ok := parents[state.ID].(map[string]any)
			if !ok {
				t.Fatalf("parent missing: %+v", parents)
			}
			types, ok := parent["visible_types"].(map[string]any)
			if !ok || types["n"] != "int" || len(types) != 1 {
				t.Fatalf("wrong visible types: %+v", parent)
			}
		})
	}
}

func TestOpaqueIDsPreserveTraceParentLinks(t *testing.T) {
	target := "package main ; func f0 ( a int , b int ) int { return a - b ; }"
	nodes := map[string]string{"root": ""}
	invalid := ""
	result, err := Search(context.Background(), subtractionSpec(t), scripted(target), Options{Strategy: "lds", Trace: func(e Event) {
		if e.Reason == "forced" || e.Reason == "active" {
			if _, ok := nodes[e.ParentID]; !ok {
				invalid = "unknown parent " + e.ParentID
			}
			if len(e.ID) != 17 {
				invalid = "unbounded generated ID " + e.ID
			}
			if previous, ok := nodes[e.ID]; ok && previous != e.ParentID {
				invalid = "collision " + e.ID
			}
			nodes[e.ID] = e.ParentID
		}
		if e.Reason == "visit" {
			if _, ok := nodes[e.ID]; !ok {
				invalid = "unknown visited node " + e.ID
			}
		}
	}}, func(s string) (bool, error) { return s == target, nil })
	if err != nil || result.Source != target || invalid != "" {
		t.Fatalf("result=%+v err=%v invalid=%s", result, err, invalid)
	}
}

func TestModelContextOmitsUnknownTypesAndPreservesLegacy(t *testing.T) {
	p := contractFixture(t, []string{"n"}, []string{"int"}, "int")
	p.ReturnPolicy = tokenspec.ReturnPolicyValuesOnly
	s := semanticState(t, p, "package main ; func f0 ( n int ) int { v0 := n ;")
	s.semantic = nil
	context := parentDecisionContext(p, s)
	if value, ok := context["visible_types"]; ok {
		if typed, ok := value.(map[string]string); !ok || typed["v0"] != "" {
			t.Fatalf("unknown type sent: %+v", context)
		}
	}
	p.ReturnPolicy = ""
	context = parentDecisionContext(p, s)
	if _, ok := context["visible_types"]; ok {
		t.Fatalf("legacy prompt changed: %+v", context)
	}
}
