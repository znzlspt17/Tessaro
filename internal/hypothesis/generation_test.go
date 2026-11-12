package hypothesis

import (
	"context"
	"encoding/json"
	"errors"
	"jevcancode/internal/rank"
	"jevcancode/internal/tokenspec"
	"math/rand"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

type generationTestRanker struct{}

func (generationTestRanker) Rank(_ context.Context, _ string, c []rank.Candidate) ([]rank.Score, error) {
	out := make([]rank.Score, len(c))
	for i, x := range c {
		out[i] = rank.Score{ID: x.ID, Score: float64(i) / float64(len(c))}
	}
	return out, nil
}

func TestGenerationGlobalBudgetPreservesUnstartedRuns(t *testing.T) {
	g := &guardedRanker{inner: &fixtureRanker{model: "jev-1.14.0"}, limit: 0}
	got, err := runGeneration(context.Background(), []frozenProblem{generationProblem(t)}, g, budgets{MaxCalls: 64, MaxTokens: 256, MaxCandidates: 8192, TimeoutSeconds: 10}, nil)
	if !errors.Is(err, errRequestBudget) || len(got) != 8 || got[0].Status != "budget_exhausted" || got[1].Status != "not_started" || g.used != 0 {
		t.Fatalf("result=%+v err=%v used=%d", got, err, g.used)
	}
}

func TestGenerationModelMismatchStopsRemainingRuns(t *testing.T) {
	g := &guardedRanker{inner: &fixtureRanker{model: "unexpected"}, limit: 10}
	got, err := runGeneration(context.Background(), []frozenProblem{generationProblem(t)}, g, budgets{MaxCalls: 64, MaxTokens: 256, MaxCandidates: 8192, TimeoutSeconds: 10}, nil)
	if !errors.Is(err, errModelMismatch) || got[0].Status != "model_mismatch" || got[1].Status != "not_started" {
		t.Fatalf("results=%+v err=%v", got, err)
	}
}

func TestPermutationRankerReproducible(t *testing.T) {
	cs := []rank.Candidate{{ID: "a"}, {ID: "b"}, {ID: "c"}, {ID: "d"}}
	a := &permutationRanker{rng: rand.New(rand.NewSource(42))}
	b := &permutationRanker{rng: rand.New(rand.NewSource(42))}
	for i := 0; i < 3; i++ {
		x, e := a.Rank(context.Background(), "", cs)
		if e != nil {
			t.Fatal(e)
		}
		y, e := b.Rank(context.Background(), "", cs)
		if e != nil || !reflect.DeepEqual(x, y) {
			t.Fatalf("x=%+v y=%+v err=%v", x, y, e)
		}
	}
}

func TestHeldoutSnapshotUnaffectedByLaterFileChange(t *testing.T) {
	p := generationProblem(t)
	p.heldoutPath = filepath.Join(t.TempDir(), "heldout.json")
	if err := os.WriteFile(p.heldoutPath, []byte(`[{"inputs":{"a":2},"want":2}]`), 0600); err != nil {
		t.Fatal(err)
	}
	cache := map[string]heldoutSnapshot{}
	first := loadHeldout(cache, p)
	if first.err != nil {
		t.Fatal(first.err)
	}
	if err := os.WriteFile(p.heldoutPath, []byte(`[{"inputs":{"a":3},"want":3}]`), 0600); err != nil {
		t.Fatal(err)
	}
	second := loadHeldout(cache, p)
	if second.err != nil || first.hash == "" || second.hash != first.hash || !reflect.DeepEqual(first.tests, second.tests) {
		t.Fatalf("first=%+v second=%+v", first, second)
	}
}

type generationScript struct{ privateSeen bool }

type generationDecisionScript struct{ generationScript }

func (r *generationDecisionScript) Metadata() rank.Metadata {
	return rank.Metadata{ReturnedModel: "jev-1.14.0"}
}
func (r *generationDecisionScript) RankDecision(ctx context.Context, state string, cs []rank.Candidate) (rank.Decision, error) {
	scores, err := r.Rank(ctx, state, cs)
	if err != nil {
		return rank.Decision{}, err
	}
	best := 0
	for i := range scores {
		if scores[i].Score > scores[best].Score {
			best = i
		}
	}
	return rank.Decision{Scores: scores, ChosenID: scores[best].ID}, nil
}

func (r *generationScript) Rank(_ context.Context, state string, cs []rank.Candidate) ([]rank.Score, error) {
	if strings.Contains(state, "1007") {
		r.privateSeen = true
	}
	var input struct {
		Parents map[string]struct {
			Prefix string `json:"prefix"`
		} `json:"parents"`
	}
	if err := json.Unmarshal([]byte(state), &input); err != nil {
		return nil, err
	}
	target := "package main ; func f0 ( a int ) int { return a ; }"
	out := make([]rank.Score, len(cs))
	for i, c := range cs {
		out[i].ID = c.ID
		if strings.HasPrefix(target, strings.TrimSpace(input.Parents[c.ParentID].Prefix+" "+c.Lexeme)) {
			out[i].Score = 1
		}
	}
	return out, nil
}

func TestGenerationHeldoutAfterPublicPassAndBranchEvidence(t *testing.T) {
	p := generationProblem(t)
	p.reference.Source = "package main; func f0(a int) int { return a; }"
	p.heldoutPath = filepath.Join(t.TempDir(), "heldout.json")
	if err := os.WriteFile(p.heldoutPath, []byte(`[{"inputs":{"a":1007},"want":1007}]`), 0600); err != nil {
		t.Fatal(err)
	}
	r := &generationScript{}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var branch, terminalBranch bool
	got, err := runGeneration(ctx, []frozenProblem{p}, r, budgets{MaxCalls: 64, MaxTokens: 256, MaxCandidates: 8192, TimeoutSeconds: 60}, func(v Progress) {
		if v.Event != nil && v.Event.ID != "" && v.Event.BranchStatus == "reference_prefix" {
			branch = true
		}
		if v.Event != nil && v.Event.Reason == "public_passed" && v.Event.BranchStatus == "reference_prefix" {
			terminalBranch = true
		}
		if v.Completed == 1 {
			cancel()
		}
	})
	if err == nil || len(got) != 8 || !got[0].PublicPassed || got[0].HeldoutPassed == nil || !*got[0].HeldoutPassed || got[0].HeldoutHash == "" || r.privateSeen || !branch || !terminalBranch {
		t.Fatalf("result=%+v err=%v private=%v branch=%v terminal=%v", got[0], err, r.privateSeen, branch, terminalBranch)
	}
	if got[1].Status != "not_started" {
		t.Fatalf("unstarted=%+v", got[1])
	}
}

func TestGenerationExhaustivePublicDoesNotOpenHeldoutAndStreamsDecision(t *testing.T) {
	p := generationProblem(t)
	p.problem.ValidationMode = "exhaustive_public"
	p.heldoutPath = filepath.Join(t.TempDir(), "missing.json")
	r := &generationDecisionScript{}
	g := &guardedRanker{inner: r, limit: 100}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var request, decision bool
	got, err := runGeneration(ctx, []frozenProblem{p}, g, budgets{MaxCalls: 64, MaxTokens: 256, MaxCandidates: 8192, TimeoutSeconds: 60}, func(v Progress) {
		if v.Event != nil {
			if strings.HasSuffix(v.Event.Kind, ":request") && v.Event.State != "" && len(v.Event.Candidates) > 0 {
				request = true
			}
			if strings.HasSuffix(v.Event.Kind, ":decision") && v.Event.ChosenID != "" && len(v.Event.Scores) > 0 {
				decision = true
			}
		}
		if v.Completed == 1 {
			cancel()
		}
	})
	if err == nil || len(got) == 0 || got[0].Status != "success" || got[0].Source == "" || got[0].HeldoutPassed != nil || got[0].ActualModel != "jev-1.14.0" || !request || !decision {
		t.Fatalf("result=%+v err=%v request=%v decision=%v", got[0], err, request, decision)
	}
}
func generationProblem(t *testing.T) frozenProblem {
	t.Helper()
	p, e := tokenspec.Decode(strings.NewReader(`{"intent":"identity","inputs":["a"],"allowed_types":["int"],"public_tests":[{"inputs":{"a":1},"want":1}]}`))
	if e != nil {
		t.Fatal(e)
	}
	return frozenProblem{id: "p", problem: p}
}
func TestGenerationRunsBothStrategiesAndSeedsWithSameBudgets(t *testing.T) {
	limits := budgets{MaxCalls: 1, MaxTokens: 1, MaxCandidates: 8192, TimeoutSeconds: 10}
	got, err := runGeneration(context.Background(), []frozenProblem{generationProblem(t)}, generationTestRanker{}, limits, nil)
	if err != nil || len(got) != 8 {
		t.Fatalf("results=%+v err=%v", got, err)
	}
	seen := map[string]bool{}
	for _, r := range got {
		b, _ := json.Marshal(r)
		var item map[string]any
		_ = json.Unmarshal(b, &item)
		if item["problem_id"] != "p" {
			t.Fatalf("result %s", b)
		}
		seen[string(b)] = true
	}
	if len(seen) != 8 {
		t.Fatalf("runs not distinguished: %+v", got)
	}
}
func TestGenerationCancellationPreservesUnstarted(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	got, err := runGeneration(ctx, []frozenProblem{generationProblem(t)}, generationTestRanker{}, budgets{MaxCalls: 1, MaxTokens: 1, MaxCandidates: 8192, TimeoutSeconds: 10}, nil)
	if err == nil || len(got) != 8 {
		t.Fatalf("results=%+v err=%v", got, err)
	}
	for _, r := range got {
		if r.Status != "not_started" {
			t.Fatalf("result=%+v", r)
		}
	}
}
