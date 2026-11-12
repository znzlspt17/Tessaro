package compare

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"tessaro/internal/rank"
	"tessaro/internal/runner"
	"tessaro/internal/tokensynth"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestDecodeUTF8BOMManifest(t *testing.T) {
	input := "\xef\xbb\xbf" + `{"models":[{"id":"fake","ranker":"fake"}],"problems":[{"id":"x","spec":"x.json"}],"strategies":["greedy"]}`
	manifest, err := Decode(strings.NewReader(input))
	if err != nil || len(manifest.Models) != 1 {
		t.Fatalf("BOM manifest rejected: %v", err)
	}
}

func TestPlanDeterministicAndComplete(t *testing.T) {
	m := Manifest{Models: []Model{{ID: "a"}, {ID: "b"}}, Problems: []Problem{{ID: "x"}, {ID: "y"}}, Strategies: []string{"greedy", "beam"}, Repeats: 2, Seed: 42}
	a, b := Plan(m), Plan(m)
	if len(a) != 16 || !reflect.DeepEqual(a, b) {
		t.Fatalf("%v %v", a, b)
	}
}
func TestCancelledTrialsCountAsNotRun(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	m := Manifest{Models: []Model{{ID: "fake", Ranker: "fake"}}, Problems: []Problem{{ID: "x", Spec: "missing"}}, Strategies: []string{"greedy", "beam"}, Repeats: 1, Seed: 42}
	r := Run(ctx, ".", m, nil)
	if r.NotRunCancelled != 2 || r.Attempted != 0 || r.Total != 2 {
		t.Fatalf("%+v", r)
	}
}
func TestManifestEngineDefaultsTokenAndAcceptsAST(t *testing.T) {
	base := `{"models":[{"id":"f","ranker":"fake"}],"problems":[{"id":"p","spec":"p.json"}],"strategies":["beam"]`
	for _, tc := range []struct{ suffix, want string }{{"}", "token"}, {`,"engine":"ast"}`, "ast"}} {
		m, err := Decode(strings.NewReader(base + tc.suffix))
		if err != nil || m.Engine != tc.want {
			t.Fatalf("%+v %v", m, err)
		}
	}
}
func TestTokenManifestLoadsNamedInputSpec(t *testing.T) {
	dir := t.TempDir()
	data := `{"intent":"identity","inputs":["x"],"allowed_types":["int"],"public_tests":[{"inputs":{"x":1},"want":1}]}`
	if err := os.WriteFile(filepath.Join(dir, "p.json"), []byte(data), 0600); err != nil {
		t.Fatal(err)
	}
	m := Manifest{Engine: "token", Models: []Model{{ID: "f", Ranker: "fake"}}, Problems: []Problem{{ID: "p", Spec: "p.json"}}, Strategies: []string{"greedy"}, Repeats: 1, Seed: 42}
	r := Run(context.Background(), dir, m, nil)
	if r.Outcomes[0].Result.Status == "input_error" || r.Outcomes[0].Trial.Engine != "token" {
		t.Fatalf("%+v", r.Outcomes[0])
	}
}
func TestLegacySpecRequiresExplicitASTEngine(t *testing.T) {
	dir := t.TempDir()
	data := `{"intent":"identity","name":"F","params":[],"return_type":"int","public_tests":[{"args":[],"want":1}]}`
	if err := os.WriteFile(filepath.Join(dir, "p.json"), []byte(data), 0600); err != nil {
		t.Fatal(err)
	}
	m := Manifest{Engine: "token", Models: []Model{{ID: "f", Ranker: "fake"}}, Problems: []Problem{{ID: "p", Spec: "p.json"}}, Strategies: []string{"beam"}, Repeats: 1, Seed: 42}
	r := Run(context.Background(), dir, m, nil)
	if r.Outcomes[0].Result.Status != "input_error" || r.Outcomes[0].Error != "input_invalid" || r.Outcomes[0].Result.ErrorCode != "input_invalid" {
		t.Fatalf("%+v", r.Outcomes[0])
	}
}
func TestTokenBatchContinuesAfterRejectedProblem(t *testing.T) {
	dir := t.TempDir()
	data := `{"intent":"identity","inputs":["x"],"allowed_types":["int"],"public_tests":[{"inputs":{"x":1},"want":1}],"budgets":{"max_candidates":1}}`
	for _, name := range []string{"one.json", "two.json"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
	}
	m := Manifest{Engine: "token", Models: []Model{{ID: "f", Ranker: "fake"}}, Problems: []Problem{{ID: "one", Spec: "one.json"}, {ID: "two", Spec: "two.json"}}, Strategies: []string{"beam"}, Repeats: 1, Seed: 42}
	report := Run(context.Background(), dir, m, nil)
	if report.Attempted != 2 || len(report.Outcomes) != 2 {
		t.Fatalf("%+v", report)
	}
	for _, v := range report.Outcomes {
		if v.Result.Status != "budget_exhausted" {
			t.Fatalf("%+v", v)
		}
	}
}

func TestTokenManifestAcceptsResearchStrategies(t *testing.T) {
	input := `{"engine":"token","models":[{"id":"fake","ranker":"fake"}],"problems":[{"id":"p","spec":"p.json"}],"strategies":["lds","levin","phs","mcts"]}`
	if _, err := Decode(strings.NewReader(input)); err != nil {
		t.Fatalf("research strategies rejected: %v", err)
	}
}

func TestPairedCrossOverOrderingAndSeeds(t *testing.T) {
	input := `{"engine":"token","models":[{"id":"f","ranker":"fake"}],"problems":[{"id":"p","spec":"p.json"}],"strategies":["lds","levin","phs"],"repeats":3,"ordering":"paired-cross-over-v1"}`
	m, err := Decode(strings.NewReader(input))
	if err != nil {
		t.Fatal(err)
	}
	plan := Plan(m)
	if len(plan) != 9 {
		t.Fatal(len(plan))
	}
	for i := 0; i < 9; i++ {
		if plan[i].Repeat != i/3+1 || plan[i].Seed != 42+int64(i/3) {
			t.Fatalf("plan=%+v", plan)
		}
	}
	if plan[0].Strategy == plan[3].Strategy || plan[3].Strategy == plan[6].Strategy {
		t.Fatalf("rotation missing: %+v", plan)
	}
}
func TestPlannedSuccessRateIncludesCancelledTrials(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	m := Manifest{Engine: "token", Models: []Model{{ID: "f", Ranker: "fake"}}, Problems: []Problem{{ID: "p", Spec: "missing"}}, Strategies: []string{"lds"}, Repeats: 2}
	data, _ := json.Marshal(Run(ctx, ".", m, nil))
	var report map[string]any
	_ = json.Unmarshal(data, &report)
	if report["planned_success_rate"] != float64(0) {
		t.Fatalf("planned denominator missing: %s", data)
	}
	groups := report["groups"].([]any)
	group := groups[0].(map[string]any)
	if group["total"] != float64(2) || group["planned_success_rate"] != float64(0) {
		t.Fatalf("group denominator missing: %s", data)
	}
}

type exhaustedRanker struct{}

func (exhaustedRanker) Rank(context.Context, string, []rank.Candidate) ([]rank.Score, error) {
	return nil, &rank.SafeError{Code: "question_budget_exhausted", Message: "budget"}
}
func TestGlobalQuestionBudgetStopsRemainingTrials(t *testing.T) {
	dir := t.TempDir()
	data := `{"intent":"identity","inputs":["x"],"allowed_types":["int"],"public_tests":[{"inputs":{"x":1},"want":1}]}`
	if err := os.WriteFile(filepath.Join(dir, "p.json"), []byte(data), 0600); err != nil {
		t.Fatal(err)
	}
	m := Manifest{Engine: "token", Models: []Model{{ID: "f", Ranker: "fake"}}, Problems: []Problem{{ID: "p", Spec: "p.json"}}, Strategies: []string{"lds", "greedy"}, Repeats: 2}
	r := RunWithResolver(context.Background(), dir, m, nil, func(context.Context, Model) (rank.Ranker, error) { return exhaustedRanker{}, nil })
	if r.Attempted != 1 || r.Outcomes[0].Result.Status != "budget_exhausted" {
		t.Fatalf("budget not recognized: %+v", r)
	}
	if r.Outcomes[0].Result.Questions != 0 || r.Outcomes[0].Result.Calls != 1 {
		t.Fatalf("rejected question counted as admitted: %+v", r.Outcomes[0].Result)
	}
	for _, o := range r.Outcomes[1:] {
		if o.Result.Status != "not_run_budget" {
			t.Fatalf("remaining trial ran: %+v", o)
		}
	}
	dataBytes, _ := json.Marshal(r)
	var fields map[string]any
	_ = json.Unmarshal(dataBytes, &fields)
	if fields["not_run_budget"] != float64(3) {
		t.Fatalf("budget count: %s", dataBytes)
	}
}

func TestConfigHashIncludesRunOptionsAndIgnoresCallbacks(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	m := Manifest{Engine: "token", Models: []Model{{ID: "f", Ranker: "fake"}}, Problems: []Problem{{ID: "p", Spec: "missing"}}, Strategies: []string{"lds"}, Repeats: 1}
	resolve := func(context.Context, Model) (rank.Ranker, error) { return rank.Fake{}, nil }
	a := RunWithOptions(ctx, ".", m, nil, resolve, runner.Request{MaxCalls: 1, ReturnPolicy: "values-only-v1"})
	b := RunWithOptions(ctx, ".", m, nil, resolve, runner.Request{MaxCalls: 2, ReturnPolicy: "values-only-v1"})
	c := RunWithOptions(ctx, ".", m, nil, resolve, runner.Request{MaxCalls: 1, ReturnPolicy: "values-only-v1", TokenTrace: func(tokensynth.Event) {}})
	if a.ConfigHash == b.ConfigHash || a.ConfigHash != c.ConfigHash {
		t.Fatalf("a=%s b=%s c=%s", a.ConfigHash, b.ConfigHash, c.ConfigHash)
	}
	data, _ := json.Marshal(a)
	var fields map[string]any
	_ = json.Unmarshal(data, &fields)
	if fields["config_hash_version"] != "v2" {
		t.Fatalf("version missing: %s", data)
	}
}

func TestCompareSeparatesEvaluationModesIncludingUnrun(t *testing.T) {
	dir := t.TempDir()
	for name, mode := range map[string]string{"a": "exhaustive_public", "b": "sampled_holdout"} {
		data := fmt.Sprintf(`{"intent":"identity","inputs":["x"],"allowed_types":["int"],"public_tests":[{"inputs":{"x":1},"want":1}],"validation_mode":%q}`, mode)
		if err := os.WriteFile(filepath.Join(dir, name+".json"), []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
	}
	m := Manifest{Engine: "token", Models: []Model{{ID: "f", Ranker: "fake"}}, Problems: []Problem{{ID: "a", Spec: "a.json"}, {ID: "b", Spec: "b.json"}}, Strategies: []string{"lds"}, Repeats: 1}
	report := RunWithResolver(context.Background(), dir, m, nil, func(context.Context, Model) (rank.Ranker, error) { return exhaustedRanker{}, nil })
	data, _ := json.Marshal(report)
	var decoded struct {
		EvaluationGroups []struct {
			ValidationMode     string   `json:"validation_mode"`
			Total              int      `json:"total"`
			PlannedSuccessRate *float64 `json:"planned_success_rate"`
		} `json:"evaluation_groups"`
		Outcomes []struct {
			Result struct {
				ValidationMode string `json:"validation_mode"`
			} `json:"result"`
		} `json:"outcomes"`
	}
	_ = json.Unmarshal(data, &decoded)
	modes := map[string]int{}
	for _, g := range decoded.EvaluationGroups {
		modes[g.ValidationMode] = g.Total
		if g.PlannedSuccessRate == nil || *g.PlannedSuccessRate != 0 {
			t.Fatalf("group=%+v", g)
		}
	}
	if modes["exhaustive_public"] != 1 || modes["sampled_holdout"] != 1 || len(decoded.Outcomes) != 2 || decoded.Outcomes[0].Result.ValidationMode == "" || decoded.Outcomes[1].Result.ValidationMode == "" {
		t.Fatalf("missing mode groups: %s", data)
	}
}

type partialCompareRanker struct{ calls int }

func (r *partialCompareRanker) Rank(ctx context.Context, state string, cs []rank.Candidate) ([]rank.Score, error) {
	scores, _, err := r.RankWithUsage(ctx, state, cs)
	return scores, err
}
func (r *partialCompareRanker) RankWithUsage(_ context.Context, _ string, cs []rank.Candidate) ([]rank.Score, *rank.Usage, error) {
	r.calls++
	if r.calls > 1 {
		return nil, nil, errors.New("fixture failure")
	}
	in, out := 7, 2
	scores := make([]rank.Score, len(cs))
	for i, c := range cs {
		scores[i] = rank.Score{ID: c.ID, Score: float64(len(cs) - i)}
	}
	return scores, &rank.Usage{InputTokens: &in, OutputTokens: &out}, nil
}
func TestCompareKeepsKnownSubtotalWhenLaterUsageIsUnknown(t *testing.T) {
	dir := t.TempDir()
	data := `{"intent":"identity","inputs":["x"],"allowed_types":["int"],"public_tests":[{"inputs":{"x":1},"want":1}]}`
	if err := os.WriteFile(filepath.Join(dir, "p.json"), []byte(data), 0600); err != nil {
		t.Fatal(err)
	}
	inPrice, outPrice := 1.0, 2.0
	m := Manifest{Engine: "token", Models: []Model{{ID: "f", Ranker: "fake", InputPrice: &inPrice, OutputPrice: &outPrice}}, Problems: []Problem{{ID: "p", Spec: "p.json"}}, Strategies: []string{"lds"}, Repeats: 1}
	report := RunWithResolver(context.Background(), dir, m, nil, func(context.Context, Model) (rank.Ranker, error) { return &partialCompareRanker{}, nil })
	if len(report.Groups) != 1 {
		t.Fatal("missing group")
	}
	g := report.Groups[0]
	if g.KnownInputTokens != 7 || g.KnownOutputTokens != 2 || g.UnknownUsageCount != 1 || g.UnknownCostCount != 1 || math.Abs(g.KnownCostSubtotal-.000011) > 1e-12 {
		t.Fatalf("known subtotal discarded: %+v", g)
	}
}
