package tokensynth

import (
	"context"
	"encoding/json"
	"errors"
	"jevcancode/internal/rank"
	"jevcancode/internal/tokenspec"
	"jevcancode/internal/tokenverify"
	"math"
	"math/rand"
	"reflect"
	"strings"
	"testing"
)

func TestResearchStrategiesDoNotRejectSupportedNames(t *testing.T) {
	for _, strategy := range []string{"lds", "levin", "phs", "mcts"} {
		t.Run(strategy, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			_, err := Search(ctx, subtractionSpec(t), rank.Fake{}, Options{Strategy: strategy}, func(string) (bool, error) { return false, nil })
			if !errors.Is(err, context.Canceled) {
				t.Fatalf("expected cancellation, got %v", err)
			}
		})
	}
}

func TestLDSFindsScriptedProgram(t *testing.T) {
	target := "package main ; func f0 ( a int , b int ) int { return a - b ; }"
	result, err := Search(context.Background(), subtractionSpec(t), scripted(target), Options{Strategy: "lds", MaxCandidates: 8192, MaxCalls: 128}, func(source string) (bool, error) { return source == target, nil })
	if err != nil || result.Source != target {
		t.Fatalf("result=%+v err=%v", result, err)
	}
}

func TestProbabilityStrategiesRejectUnmarkedScores(t *testing.T) {
	for _, strategy := range []string{"levin", "phs", "mcts"} {
		t.Run(strategy, func(t *testing.T) {
			_, err := Search(context.Background(), subtractionSpec(t), rank.Fake{}, Options{Strategy: strategy}, func(string) (bool, error) { return false, nil })
			if err == nil || err.Error() != "normalized categorical probabilities required" {
				t.Fatalf("got %v", err)
			}
		})
	}
}

type scriptedCategorical struct{ scripted }

func (scriptedCategorical) NormalizedCategorical() bool { return true }
func (r scriptedCategorical) Rank(ctx context.Context, state string, cs []rank.Candidate) ([]rank.Score, error) {
	scores, err := r.scripted.Rank(ctx, state, cs)
	if err != nil {
		return nil, err
	}
	best := 0
	for i := range scores {
		if scores[i].Score > scores[best].Score {
			best = i
		}
	}
	for i := range scores {
		scores[i].Score = 0
	}
	scores[best].Score = 1
	return scores, nil
}
func TestMCTSEvaluatesOnlyCompletePublicPrograms(t *testing.T) {
	target := "package main ; func f0 ( a int , b int ) int { return a - b ; }"
	evaluations := 0
	result, err := Search(context.Background(), subtractionSpec(t), scriptedCategorical{scripted(target)}, Options{Strategy: "mcts", Seed: 42, PublicEvaluate: func(_ context.Context, source string) (tokenverify.PublicEvaluation, error) {
		evaluations++
		if !strings.HasSuffix(source, "}") {
			t.Fatalf("incomplete rollout: %q", source)
		}
		if source == target {
			return tokenverify.PublicEvaluation{Status: "completed", Passed: 1, Total: 1}, nil
		}
		return tokenverify.PublicEvaluation{Status: "completed", Total: 1}, nil
	}}, func(source string) (bool, error) { return source == target, nil })
	if err != nil || result.Source != target || evaluations == 0 {
		t.Fatalf("result=%+v evals=%d err=%v", result, evaluations, err)
	}
}

type roundedCategorical struct{}

func (roundedCategorical) NormalizedCategorical() bool { return true }
func (roundedCategorical) Rank(_ context.Context, _ string, cs []rank.Candidate) ([]rank.Score, error) {
	ss := make([]rank.Score, len(cs))
	for i, c := range cs {
		ss[i] = rank.Score{ID: c.ID, Score: math.Round((1/float64(len(cs)))*10000) / 10000}
	}
	return ss, nil
}
func TestLevinAcceptsValidRoundedCategorical(t *testing.T) {
	_, err := Search(context.Background(), subtractionSpec(t), roundedCategorical{}, Options{Strategy: "levin", MaxCalls: 8}, func(string) (bool, error) { return false, nil })
	if !errors.Is(err, ErrBudget) {
		t.Fatalf("expected bounded search, got %v", err)
	}
}

type countDecision struct {
	calls int
	fail  bool
}

func (r *countDecision) Rank(_ context.Context, _ string, cs []rank.Candidate) ([]rank.Score, error) {
	r.calls++
	if r.fail {
		return nil, errors.New("temporary")
	}
	out := make([]rank.Score, len(cs))
	for i, c := range cs {
		out[i] = rank.Score{ID: c.ID, Score: float64(len(cs) - i)}
	}
	return out, nil
}
func TestDecisionMemoReusesIdenticalParent(t *testing.T) {
	p := subtractionSpec(t)
	state, err := NewState()
	if err != nil {
		t.Fatal(err)
	}
	for _, lexeme := range strings.Fields("package main ; func f0 ( a int , b int ) int { return") {
		cs, err := Next(p, state)
		if err != nil {
			t.Fatal(err)
		}
		found := false
		for _, c := range cs {
			if c.Lexeme == lexeme {
				state, err = state.Advance(c, lexeme)
				if err != nil {
					t.Fatal(err)
				}
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("no %s", lexeme)
		}
	}
	r := &countDecision{}
	_, err = searchFrom(context.Background(), p, r, Options{Strategy: "lds", MaxTokens: len(state.Tokens) + 1, MaxCalls: 10}, func(string) (bool, error) { return false, nil }, []*State{state, state})
	if !errors.Is(err, ErrBudget) || r.calls != 1 {
		t.Fatalf("calls=%d err=%v", r.calls, err)
	}
}

func TestResearchStrategiesRecoverAfterWrongReturn(t *testing.T) {
	p := subtractionSpec(t)
	p.ReturnPolicy = tokenspec.ReturnPolicyValuesOnly
	p.AllowedTypes = []string{"int"}
	state, err := NewState()
	if err != nil {
		t.Fatal(err)
	}
	for _, lexeme := range strings.Fields("package main ; func f0 ( a int , b int ) int { return") {
		cs, e := Next(p, state)
		if e != nil {
			t.Fatal(e)
		}
		found := false
		for _, c := range cs {
			if c.Lexeme == lexeme {
				state, e = state.Advance(c, lexeme)
				if e != nil {
					t.Fatal(e)
				}
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("no %s", lexeme)
		}
	}
	wrong := "package main ; func f0 ( a int , b int ) int { return b ; }"
	target := "package main ; func f0 ( a int , b int ) int { return a ; }"
	for _, strategy := range []string{"lds", "levin", "phs", "mcts"} {
		t.Run(strategy, func(t *testing.T) {
			o := Options{Strategy: strategy, Seed: 42, MaxTokens: len(state.Tokens) + 3, MaxCalls: 1000, MaxCandidates: 20000}
			o.PublicEvaluate = func(_ context.Context, s string) (tokenverify.PublicEvaluation, error) {
				if s == target {
					return tokenverify.PublicEvaluation{Status: "completed", Passed: 1, Total: 1}, nil
				}
				return tokenverify.PublicEvaluation{Status: "completed", Total: 1}, nil
			}
			result, e := searchFrom(context.Background(), p, scriptedCategorical{scripted(wrong)}, o, func(s string) (bool, error) { return s == target, nil }, []*State{state})
			if e != nil || result.Source != target {
				t.Fatalf("result=%+v err=%v", result, e)
			}
		})
	}
}

type tiedChoice struct{}

func (tiedChoice) Rank(_ context.Context, _ string, cs []rank.Candidate) ([]rank.Score, error) {
	ss := make([]rank.Score, len(cs))
	for i, c := range cs {
		ss[i] = rank.Score{ID: c.ID, Score: 1}
	}
	return ss, nil
}
func (r tiedChoice) RankDecision(ctx context.Context, s string, cs []rank.Candidate) (rank.Decision, error) {
	scores, _ := r.Rank(ctx, s, cs)
	return rank.Decision{Scores: scores, ChosenID: cs[len(cs)-1].ID}, nil
}
func TestLDSChosenTieIsFirstRankAndTraced(t *testing.T) {
	var ranked []Event
	_, err := Search(context.Background(), subtractionSpec(t), tiedChoice{}, Options{Strategy: "lds", MaxCalls: 1, Trace: func(e Event) {
		if e.Reason == "ranked" {
			ranked = append(ranked, e)
		}
	}}, func(string) (bool, error) { return false, nil })
	if !errors.Is(err, ErrBudget) || len(ranked) < 2 || ranked[0].Rank != 1 || ranked[0].ID != ranked[len(ranked)-1].ChosenID {
		t.Fatalf("ranked=%+v err=%v", ranked, err)
	}
}

type returnChoiceRecorder struct{ seen []string }

func (*returnChoiceRecorder) NormalizedCategorical() bool { return true }

func (r *returnChoiceRecorder) Rank(_ context.Context, _ string, cs []rank.Candidate) ([]rank.Score, error) {
	for _, c := range cs {
		r.seen = append(r.seen, c.Lexeme)
	}
	return nil, errors.New("stop")
}
func TestValuesOnlyReturnPrefixNeverReachesRanker(t *testing.T) {
	p := subtractionSpec(t)
	p.ReturnPolicy = tokenspec.ReturnPolicyValuesOnly
	state, err := NewState()
	if err != nil {
		t.Fatal(err)
	}
	for _, lexeme := range strings.Fields("package main ; func f0 ( a int , b int ) int { return a") {
		cs, e := Next(p, state)
		if e != nil {
			t.Fatal(e)
		}
		found := false
		for _, c := range cs {
			if c.Lexeme == lexeme {
				state, e = state.Advance(c, lexeme)
				if e != nil {
					t.Fatal(e)
				}
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("no %s", lexeme)
		}
	}
	for _, strategy := range []string{"beam", "greedy", "strict-greedy", "lds", "levin", "phs", "mcts"} {
		t.Run(strategy, func(t *testing.T) {
			r := &returnChoiceRecorder{}
			_, _ = searchFrom(context.Background(), p, r, Options{Strategy: strategy, PublicEvaluate: func(context.Context, string) (tokenverify.PublicEvaluation, error) {
				return tokenverify.PublicEvaluation{}, nil
			}}, func(string) (bool, error) { return false, nil }, []*State{state})
			for _, lexeme := range r.seen {
				if lexeme == "+" || lexeme == "(" || lexeme == "[" {
					t.Fatalf("forbidden %s seen in %v", lexeme, r.seen)
				}
			}
		})
	}
}

type inconsistentDecision struct{}

func (inconsistentDecision) Rank(_ context.Context, _ string, cs []rank.Candidate) ([]rank.Score, error) {
	ss := make([]rank.Score, len(cs))
	for i, c := range cs {
		ss[i] = rank.Score{ID: c.ID, Score: float64(len(cs) - i)}
	}
	return ss, nil
}
func (r inconsistentDecision) RankDecision(ctx context.Context, s string, cs []rank.Candidate) (rank.Decision, error) {
	scores, _ := r.Rank(ctx, s, cs)
	return rank.Decision{Scores: scores, ChosenID: cs[len(cs)-1].ID}, nil
}
func TestResearchRejectsChoiceBelowMaximum(t *testing.T) {
	_, err := Search(context.Background(), subtractionSpec(t), inconsistentDecision{}, Options{Strategy: "lds"}, func(string) (bool, error) { return false, nil })
	if err == nil || !strings.Contains(err.Error(), "chosen") {
		t.Fatalf("inconsistent choice accepted: %v", err)
	}
}

func TestMCTSReportsCompletedPartialRewardAndIgnoresFailedCounts(t *testing.T) {
	target := "package main ; func f0 ( a int , b int ) int { return a - b ; }"
	for _, tc := range []struct {
		status string
		want   float64
	}{{"completed", .5}, {"execution_failed", 0}, {"execution_timeout", 0}} {
		t.Run(tc.status, func(t *testing.T) {
			var scores []float64
			calls := 0
			_, _ = Search(context.Background(), subtractionSpec(t), scriptedCategorical{scripted(target)}, Options{Strategy: "mcts", Seed: 42, MaxCalls: 50, Trace: func(e Event) {
				if e.Reason == "public_evaluated" && e.Score != nil {
					scores = append(scores, *e.Score)
				}
			}, PublicEvaluate: func(context.Context, string) (tokenverify.PublicEvaluation, error) {
				calls++
				if calls > 1 {
					return tokenverify.PublicEvaluation{}, context.Canceled
				}
				return tokenverify.PublicEvaluation{Status: tc.status, Passed: 1, Total: 2}, nil
			}}, func(string) (bool, error) { t.Fatal("partial result submitted"); return false, nil })
			if len(scores) == 0 || scores[0] != tc.want {
				t.Fatalf("scores=%v want=%v", scores, tc.want)
			}
		})
	}
}

func TestResearchStrategiesVisitDifferently(t *testing.T) {
	visits := map[string][]string{}
	for _, strategy := range []string{"lds", "levin", "phs", "mcts"} {
		t.Run(strategy, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			o := Options{Strategy: strategy, Seed: 42, MaxCalls: 20, TraceCode: true, RemainingTokens: func(s *State) int {
				if len(s.Tokens) >= 8 && s.Tokens[len(s.Tokens)-1] == "(" {
					return 100
				}
				return 0
			}, PublicEvaluate: func(context.Context, string) (tokenverify.PublicEvaluation, error) {
				return tokenverify.PublicEvaluation{Status: "completed", Total: 1}, nil
			}}
			o.Trace = func(e Event) {
				if e.Reason == "visit" {
					visits[strategy] = append(visits[strategy], e.Code)
					if len(visits[strategy]) >= 20 {
						cancel()
					}
				}
			}
			_, err := Search(ctx, subtractionSpec(t), roundedCategorical{}, o, func(string) (bool, error) { return false, nil })
			if !errors.Is(err, context.Canceled) && !errors.Is(err, ErrBudget) {
				t.Fatalf("err=%v", err)
			}
		})
	}
	if reflect.DeepEqual(visits["lds"], visits["levin"]) {
		t.Fatalf("LDS and Levin visited identically: %v", visits["lds"])
	}
	if reflect.DeepEqual(visits["levin"], visits["phs"]) {
		t.Fatalf("PHS bound did not change traversal: %v", visits["levin"])
	}
	if reflect.DeepEqual(visits["mcts"], visits["levin"]) {
		t.Fatalf("MCTS and Levin visited identically: %v", visits["mcts"])
	}
}

func TestMCTSTreeBackpropagationAndDeadLeaf(t *testing.T) {
	root := &searchNode{}
	branch := &searchNode{parent: root, prior: .7}
	leaf := &searchNode{parent: branch, prior: 1}
	sibling := &searchNode{parent: root, prior: .3}
	root.children = []*searchNode{branch, sibling}
	branch.children = []*searchNode{leaf}
	retire(leaf, .5)
	if root.visits != 1 || branch.visits != 1 || leaf.visits != 1 || root.value != .5 || branch.value != .5 || leaf.value != .5 || !leaf.dead || !branch.dead || root.dead {
		t.Fatalf("root=%+v branch=%+v leaf=%+v", root, branch, leaf)
	}
	if got := selectMCTS(root, rand.New(rand.NewSource(42))); got != sibling {
		t.Fatalf("selected dead leaf or wrong sibling: %p", got)
	}
}
func TestMCTSFixedSeedRepeatsSelection(t *testing.T) {
	root := &searchNode{}
	for _, p := range []float64{.5, .3, .2} {
		root.children = append(root.children, &searchNode{parent: root, prior: p})
	}
	first := selectMCTS(root, rand.New(rand.NewSource(42)))
	second := selectMCTS(root, rand.New(rand.NewSource(42)))
	if first != second {
		t.Fatal("same seed selected different nodes")
	}
}

func TestMCTSStopsOnEvaluatorInfrastructureError(t *testing.T) {
	target := "package main ; func f0 ( a int , b int ) int { return a - b ; }"
	sentinel := errors.New("unavailable infrastructure")
	calls := 0
	_, err := Search(context.Background(), subtractionSpec(t), scriptedCategorical{scripted(target)}, Options{Strategy: "mcts", Seed: 42, PublicEvaluate: func(context.Context, string) (tokenverify.PublicEvaluation, error) {
		calls++
		return tokenverify.PublicEvaluation{}, sentinel
	}}, func(string) (bool, error) { t.Fatal("invalid evaluation submitted"); return false, nil })
	if !errors.Is(err, sentinel) || calls != 1 {
		t.Fatalf("err=%v calls=%d", err, calls)
	}
}

func TestMCTSVisitTraceRepeatsForSeed(t *testing.T) {
	run := func() []string {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		var visits []string
		_, _ = Search(ctx, subtractionSpec(t), roundedCategorical{}, Options{Strategy: "mcts", Seed: 42, TraceCode: true, Trace: func(e Event) {
			if e.Reason == "visit" {
				visits = append(visits, e.Code)
				if len(visits) == 25 {
					cancel()
				}
			}
		}, PublicEvaluate: func(context.Context, string) (tokenverify.PublicEvaluation, error) {
			return tokenverify.PublicEvaluation{Status: "completed", Total: 1}, nil
		}}, func(string) (bool, error) { return false, nil })
		return visits
	}
	first, second := run(), run()
	if len(first) != 25 || !reflect.DeepEqual(first, second) {
		t.Fatalf("first=%v second=%v", first, second)
	}
}

func TestMCTSFinishesRolloutBeforeReselectingTree(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	last := ""
	visits := 0
	evaluated := false
	diverged := false
	_, _ = Search(ctx, subtractionSpec(t), roundedCategorical{}, Options{Strategy: "mcts", Seed: 42, MaxCalls: 64, Trace: func(e Event) {
		if e.Reason == "public_evaluated" {
			evaluated = true
		}
		if e.Reason == "visit" && !evaluated {
			if last != "" && e.ParentID != last {
				diverged = true
				cancel()
			}
			last = e.ID
			visits++
			if visits >= 25 {
				cancel()
			}
		}
	}, PublicEvaluate: func(context.Context, string) (tokenverify.PublicEvaluation, error) {
		return tokenverify.PublicEvaluation{Status: "completed", Total: 1}, nil
	}}, func(string) (bool, error) { return false, nil })
	if diverged {
		t.Fatalf("reselected another tree branch before first complete rollout after %d visits", visits)
	}
	if visits < 2 {
		t.Fatalf("insufficient rollout visits: %d", visits)
	}
}

func TestMCTSFirstSimulationCompletesBeforeReselection(t *testing.T) {
	target := "package main ; func f0 ( a int , b int ) int { return a - b ; }"
	last := ""
	evaluated := false
	diverged := false
	result, err := Search(context.Background(), subtractionSpec(t), scriptedCategorical{scripted(target)}, Options{Strategy: "mcts", Seed: 42, MaxCalls: 30, Trace: func(e Event) {
		if e.Reason == "public_evaluated" {
			evaluated = true
		}
		if e.Reason == "visit" && !evaluated {
			if last != "" && e.ParentID != last {
				diverged = true
			}
			last = e.ID
		}
	}, PublicEvaluate: func(_ context.Context, s string) (tokenverify.PublicEvaluation, error) {
		if s == target {
			return tokenverify.PublicEvaluation{Status: "completed", Passed: 1, Total: 1}, nil
		}
		return tokenverify.PublicEvaluation{Status: "completed", Total: 1}, nil
	}}, func(s string) (bool, error) { return s == target, nil })
	if err != nil || !evaluated || diverged || result.Source != target {
		t.Fatalf("err=%v evaluated=%v diverged=%v result=%+v", err, evaluated, diverged, result)
	}
}

func TestMCTSRejectsUnsupportedDecisionBatch(t *testing.T) {
	_, err := Search(context.Background(), subtractionSpec(t), roundedCategorical{}, Options{Strategy: "mcts", DecisionBatchSize: 2, PublicEvaluate: func(context.Context, string) (tokenverify.PublicEvaluation, error) {
		return tokenverify.PublicEvaluation{}, nil
	}}, func(string) (bool, error) { return false, nil })
	if err == nil || !strings.Contains(err.Error(), "batch") {
		t.Fatalf("unsupported batch accepted: %v", err)
	}
}

type batchRecorder struct{ singles, batches, questions int }

func (r *batchRecorder) Rank(_ context.Context, _ string, cs []rank.Candidate) ([]rank.Score, error) {
	r.singles++
	ss := make([]rank.Score, len(cs))
	for i, c := range cs {
		ss[i] = rank.Score{ID: c.ID, Score: float64(len(cs) - i)}
	}
	return ss, nil
}
func (r *batchRecorder) RankDecisions(ctx context.Context, qs []rank.DecisionRequest) ([]rank.Decision, *rank.Usage, error) {
	r.batches++
	r.questions += len(qs)
	out := make([]rank.Decision, len(qs))
	for i, q := range qs {
		ss := make([]rank.Score, len(q.Candidates))
		for j, c := range q.Candidates {
			ss[j] = rank.Score{ID: c.ID, Score: float64(len(ss) - j)}
		}
		out[i] = rank.Decision{Scores: ss}
	}
	return out, nil, nil
}
func (r *batchRecorder) SupportsDecisionBatch() bool { return true }
func TestLDSPrefetchBatchesIndependentParentsWithinBudget(t *testing.T) {
	p := subtractionSpec(t)
	state, err := NewState()
	if err != nil {
		t.Fatal(err)
	}
	for _, lexeme := range strings.Fields("package main ; func f0 ( a int , b int ) int { return") {
		cs, e := Next(p, state)
		if e != nil {
			t.Fatal(e)
		}
		found := false
		for _, c := range cs {
			if c.Lexeme == lexeme {
				state, e = state.Advance(c, lexeme)
				if e != nil {
					t.Fatal(e)
				}
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("no %s", lexeme)
		}
	}
	other := *state
	other.ID = "other"
	r := &batchRecorder{}
	var events []Event
	result, err := searchFrom(context.Background(), p, r, Options{Strategy: "lds", DecisionBatchSize: 2, MaxTokens: len(state.Tokens) + 1, MaxCalls: 2, Trace: func(e Event) { events = append(events, e) }}, func(string) (bool, error) { return false, nil }, []*State{state, &other})
	if !errors.Is(err, ErrBudget) || r.batches != 1 || r.questions != 2 || r.singles != 0 || result.Calls != 2 {
		t.Fatalf("result=%+v calls=%+v err=%v", result, r, err)
	}
	foundPrefetch := false
	for _, e := range events {
		if e.Reason == "prefetch_context" {
			foundPrefetch = true
			if e.Phase != "prefetch" || e.ParentID != "other" || e.DecisionContext["question_id"] != "q1" || e.DecisionContext["transport"] != "batch" {
				t.Fatalf("prefetch trace mismatch: %+v", e)
			}
		}
	}
	if !foundPrefetch {
		t.Fatal("missing prefetch trace")
	}
	limited := &batchRecorder{}
	result, err = searchFrom(context.Background(), p, limited, Options{Strategy: "lds", DecisionBatchSize: 8, MaxTokens: len(state.Tokens) + 1, MaxCalls: 1}, func(string) (bool, error) { return false, nil }, []*State{state, &other})
	if !errors.Is(err, ErrBudget) || result.Calls != 1 || limited.batches != 0 || limited.singles != 1 {
		t.Fatalf("budget escaped: result=%+v calls=%+v err=%v", result, limited, err)
	}
	candidateLimited := &batchRecorder{}
	result, err = searchFrom(context.Background(), p, candidateLimited, Options{Strategy: "lds", DecisionBatchSize: 8, MaxTokens: len(state.Tokens) + 1, MaxCalls: 8, MaxCandidates: 26}, func(string) (bool, error) { return false, nil }, []*State{state, &other})
	if !errors.Is(err, ErrBudget) || result.Candidates > 26 {
		t.Fatalf("candidate budget escaped: %+v err=%v", result, err)
	}
}

func TestBatchAndSinglePreserveDeterministicParentRanking(t *testing.T) {
	p := subtractionSpec(t)
	state, err := NewState()
	if err != nil {
		t.Fatal(err)
	}
	for _, lexeme := range strings.Fields("package main ; func f0 ( a int , b int ) int { return") {
		cs, e := Next(p, state)
		if e != nil {
			t.Fatal(e)
		}
		found := false
		for _, c := range cs {
			if c.Lexeme == lexeme {
				state, e = state.Advance(c, lexeme)
				if e != nil {
					t.Fatal(e)
				}
				found = true
				break
			}
		}
		if !found {
			t.Fatal(lexeme)
		}
	}
	other := *state
	other.ID = "other"
	run := func(size int) map[string][]string {
		visits := map[string][]string{}
		r := &batchRecorder{}
		_, e := searchFrom(context.Background(), p, r, Options{Strategy: "lds", DecisionBatchSize: size, MaxCalls: 2, MaxTokens: len(state.Tokens) + 1, Trace: func(event Event) {
			if event.Reason == "ranked" {
				visits[event.ParentID] = append(visits[event.ParentID], event.Lexeme)
			}
		}}, func(string) (bool, error) { return false, nil }, []*State{state, &other})
		if !errors.Is(e, ErrBudget) {
			t.Fatal(e)
		}
		return visits
	}
	single, batch := run(1), run(2)
	if len(single) != 2 || !reflect.DeepEqual(single, batch) {
		t.Fatalf("single=%v batch=%v", single, batch)
	}
}

func TestResearchTraceReportsMonotonicDecisionLifecycle(t *testing.T) {
	var events []Event
	_, err := Search(context.Background(), subtractionSpec(t), tiedChoice{}, Options{Strategy: "lds", MaxCalls: 3, Trace: func(e Event) { events = append(events, e) }}, func(string) (bool, error) { return false, nil })
	if !errors.Is(err, ErrBudget) {
		t.Fatal(err)
	}
	contexts, results, starts, frontiers := 0, 0, 0, 0
	last := 0
	for _, e := range events {
		if e.Step < last {
			t.Fatalf("step regressed: %d -> %d, event=%+v", last, e.Step, e)
		}
		if e.Step > last {
			last = e.Step
		}
		switch e.Reason {
		case "decision_context":
			contexts++
			if e.DecisionContext["instructions"] == nil || e.DecisionContext["criteria"] == nil {
				t.Fatalf("missing question context %+v", e)
			}
		case "model_started":
			starts++
		case "decision_result":
			results++
			if e.DecisionStatus != "received" || e.ChosenID == "" {
				t.Fatalf("missing actual choice %+v", e)
			}
		case "frontier":
			frontiers++
		}
	}
	if contexts < 2 || contexts != results || starts != contexts || frontiers == 0 {
		t.Fatalf("contexts=%d results=%d starts=%d frontier=%d", contexts, results, starts, frontiers)
	}
}

type policyStateRecorder struct{ states []map[string]any }

func (*policyStateRecorder) NormalizedCategorical() bool { return true }
func (r *policyStateRecorder) Rank(_ context.Context, state string, cs []rank.Candidate) ([]rank.Score, error) {
	var value map[string]any
	if err := json.Unmarshal([]byte(state), &value); err != nil {
		return nil, err
	}
	r.states = append(r.states, value)
	return nil, errors.New("stop")
}
func TestValuesOnlyPolicyIsVisibleToModel(t *testing.T) {
	for _, strategy := range []string{"beam", "lds", "levin", "phs", "mcts"} {
		t.Run(strategy, func(t *testing.T) {
			p := subtractionSpec(t)
			p.ReturnPolicy = tokenspec.ReturnPolicyValuesOnly
			r := &policyStateRecorder{}
			_, _ = Search(context.Background(), p, r, Options{Strategy: strategy, PublicEvaluate: func(context.Context, string) (tokenverify.PublicEvaluation, error) {
				return tokenverify.PublicEvaluation{}, nil
			}}, func(string) (bool, error) { return false, nil })
			if len(r.states) != 1 || r.states[0]["return_policy"] != tokenspec.ReturnPolicyValuesOnly || r.states[0]["return_policy_instructions"] == nil {
				t.Fatalf("policy absent: %+v", r.states)
			}
		})
	}
}

func TestCancellationStopsFurtherSiblingExpansion(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	active := 0
	_, err := Search(ctx, subtractionSpec(t), rank.Fake{}, Options{Strategy: "lds", Trace: func(e Event) {
		if e.Reason == "active" {
			active++
			if active == 1 {
				cancel()
			}
		}
	}}, func(string) (bool, error) { return false, nil })
	if !errors.Is(err, context.Canceled) || active != 1 {
		t.Fatalf("err=%v active expansions=%d", err, active)
	}
}

func TestDecisionTraceSchemaForAllStrategies(t *testing.T) {
	for _, strategy := range []string{"beam", "greedy", "strict-greedy", "lds", "levin", "phs", "mcts"} {
		t.Run(strategy, func(t *testing.T) {
			var got map[string]any
			_, _ = Search(context.Background(), subtractionSpec(t), scriptedCategorical{scripted("package main ; func f0 ( a int , b int ) int { return a ; }")}, Options{Strategy: strategy, MaxCalls: 1, Trace: func(e Event) {
				if e.Reason == "decision_context" {
					got = e.DecisionContext
				}
			}, PublicEvaluate: func(context.Context, string) (tokenverify.PublicEvaluation, error) {
				return tokenverify.PublicEvaluation{}, nil
			}}, func(string) (bool, error) { return false, nil })
			if got["schema_version"] != "decision-context-v1" || got["transport"] != "single" || got["state_json"] == nil || got["ordered_candidates"] == nil {
				t.Fatalf("schema missing: %+v", got)
			}
		})
	}
}
