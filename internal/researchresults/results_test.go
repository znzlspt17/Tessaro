package researchresults

import (
	"encoding/json"
	"fmt"
	"jevcancode/internal/compare"
	"jevcancode/internal/rank"
	"jevcancode/internal/runner"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

var testConfig = config{shards: 2, problemsPerShard: 1, repeats: 1, dev: 1, eval: 1, strategies: []string{"strict-greedy", "beam"}, seeds: []int64{42}}

func put(t *testing.T, path string, value any) {
	t.Helper()
	b, e := json.Marshal(value)
	if e != nil {
		t.Fatal(e)
	}
	if e = os.MkdirAll(filepath.Dir(path), 0755); e != nil {
		t.Fatal(e)
	}
	if e = os.WriteFile(path, b, 0600); e != nil {
		t.Fatal(e)
	}
}
func fixture(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	p := Protocol{Kind: "fixed-final-study", Problems: 2, Strategies: testConfig.strategies, Repeats: 1, Seeds: []int64{42}, PlannedTrials: 4, Shards: 2, ProblemsPerShard: 1, MaxCalls: 64, MaxCandidates: 8192, MaxTokens: 256, TimeoutSeconds: 300, DecisionBatchSize: 1, ReturnPolicy: "values-only-v1", Model: "jev-1.14.0"}
	put(t, filepath.Join(dir, "protocol.json"), p)
	for i := 0; i < 2; i++ {
		split := "dev"
		if i == 1 {
			split = "eval"
		}
		id := fmt.Sprintf("p%d", i)
		prefix := fmt.Sprintf("shard-%02d", i)
		run := "run001"
		model := compare.Model{ID: "jev", Ranker: "jev", Model: "jev-1.14.0"}
		m := compare.Manifest{Engine: "token", Models: []compare.Model{model}, Problems: []compare.Problem{{ID: id, Spec: split + "/specs/" + id + ".json", Heldout: split + "/heldout/" + id + ".json"}}, Strategies: testConfig.strategies, Repeats: 1, Seed: 42, Ordering: "paired-cross-over-v1"}
		put(t, filepath.Join(dir, prefix+".manifest.json"), m)
		var outcomes []compare.Outcome
		for j, strategy := range testConfig.strategies {
			status := "success"
			if i == 1 && j == 0 {
				status = "not_run_budget"
			}
			if i == 1 && j == 1 {
				status = "model_error"
			}
			v := int64(2)
			in, out := 10, 3
			passed := true
			result := runner.Result{Status: status, PublicPassed: status == "success", ValidationMode: "sampled_holdout", Questions: 3, HTTPRequests: &v, Usage: &rank.Usage{InputTokens: &in, OutputTokens: &out}}
			if status == "success" {
				result.HeldoutPassed = &passed
			}
			outcomes = append(outcomes, compare.Outcome{TrialIndex: j + 1, Trial: compare.Trial{Engine: "token", Model: "jev", Problem: id, Strategy: strategy, Repeat: 1, Seed: 42}, Result: result})
		}
		report := compare.Report{Engine: "token", Seed: 42, Total: 2, RunOptions: compare.RunOptions{ReturnPolicy: "values-only-v1", DecisionBatchSize: 1, MaxTokens: 256, MaxCandidates: 8192, MaxCalls: 64, TimeoutSeconds: 300, TrialSeedBase: 42}, Outcomes: outcomes}
		put(t, filepath.Join(dir, prefix+".summary.json"), map[string]any{"status": "completed", "run_id": run, "report": report})
		put(t, filepath.Join(dir, prefix, run), map[string]any{"run_id": run, "status": "completed", "job_kind": "compare", "payload": report})
	}
	return dir
}
func TestLoadAggregatesCompletedFixtures(t *testing.T) {
	s, e := load(fixture(t), testConfig)
	if e != nil {
		t.Fatal(e)
	}
	if len(s.Trials) != 4 || len(s.Strategies) != 2 {
		t.Fatalf("%+v", s)
	}
	g := s.Strategies[0]
	if g.Strategy != "strict-greedy" || g.All.Planned != 2 || g.All.Observed != 2 || g.All.Attempted != 1 || g.All.Success != 1 || g.All.NotRun != 1 || g.Dev.Success != 1 || g.Eval.NotRun != 1 || g.All.Questions != 3 || g.All.HTTPRequests != 2 || g.All.KnownInputTokens != 10 {
		t.Fatalf("%+v", g)
	}
	if s.Trials[0].TraceAvailable {
		t.Fatal("missing optional trace reported present")
	}
}
func TestLoadRejectsMissingDuplicateAndConditionMismatch(t *testing.T) {
	for _, tc := range []string{"missing", "duplicate", "policy"} {
		t.Run(tc, func(t *testing.T) {
			dir := fixture(t)
			path := filepath.Join(dir, "shard-00.summary.json")
			if tc == "missing" {
				os.Remove(path)
			} else {
				b, e := os.ReadFile(path)
				if e != nil {
					t.Fatal(e)
				}
				var x map[string]any
				if e = json.Unmarshal(b, &x); e != nil {
					t.Fatal(e)
				}
				report := x["report"].(map[string]any)
				if tc == "duplicate" {
					arr := report["outcomes"].([]any)
					arr[1] = arr[0]
				} else {
					report["run_options"].(map[string]any)["return_policy"] = "legacy"
				}
				put(t, path, x)
			}
			if _, e := load(dir, testConfig); e == nil || !strings.Contains(strings.ToLower(e.Error()), tc) {
				t.Fatalf("want %s error, got %v", tc, e)
			}
		})
	}
}

func TestLoadRejectsInconsistentSuccess(t *testing.T) {
	dir := fixture(t)
	summaryPath := filepath.Join(dir, "shard-00.summary.json")
	b, err := os.ReadFile(summaryPath)
	if err != nil {
		t.Fatal(err)
	}
	var summary map[string]any
	if err = json.Unmarshal(b, &summary); err != nil {
		t.Fatal(err)
	}
	report := summary["report"].(map[string]any)
	rows := report["outcomes"].([]any)
	rows[0].(map[string]any)["result"].(map[string]any)["public_passed"] = false
	put(t, summaryPath, summary)
	put(t, filepath.Join(dir, "shard-00", "run001"), map[string]any{"run_id": "run001", "status": "completed", "job_kind": "compare", "payload": report})
	if _, err = load(dir, testConfig); err == nil || !strings.Contains(err.Error(), "success") {
		t.Fatalf("expected inconsistent success, got %v", err)
	}
}

func TestLoadRejectsInconsistentEffectiveBudgets(t *testing.T) {
	dir := fixture(t)
	path := filepath.Join(dir, "shard-00.summary.json")
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var summary map[string]any
	if err = json.Unmarshal(b, &summary); err != nil {
		t.Fatal(err)
	}
	report := summary["report"].(map[string]any)
	rows := report["outcomes"].([]any)
	rows[0].(map[string]any)["result"].(map[string]any)["effective_budgets"] = map[string]any{"max_calls": 65}
	put(t, path, summary)
	put(t, filepath.Join(dir, "shard-00", "run001"), map[string]any{"run_id": "run001", "status": "completed", "job_kind": "compare", "payload": report})
	if _, err = load(dir, testConfig); err == nil || !strings.Contains(err.Error(), "budget") {
		t.Fatalf("expected budget mismatch, got %v", err)
	}
}

func TestLoadRejectsMalformedJSON(t *testing.T) {
	dir := fixture(t)
	if err := os.WriteFile(filepath.Join(dir, "shard-00.summary.json"), []byte("{"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := load(dir, testConfig); err == nil || !strings.Contains(err.Error(), "invalid JSON") {
		t.Fatalf("expected invalid JSON, got %v", err)
	}
}
func TestLoadReportsTraceAvailabilityWithoutReadingTrace(t *testing.T) {
	dir := fixture(t)
	path := filepath.Join(dir, "shard-00", "run001.trace.jsonl")
	if err := os.WriteFile(path, []byte("not JSON"), 0600); err != nil {
		t.Fatal(err)
	}
	study, err := load(dir, testConfig)
	if err != nil {
		t.Fatal(err)
	}
	if !study.Trials[0].TraceAvailable || study.Trials[0].TracePath != path {
		t.Fatalf("trace metadata: %+v", study.Trials[0])
	}
}

func TestLoadRejectsConflictingModelPrice(t *testing.T) {
	dir := fixture(t)
	path := filepath.Join(dir, "shard-00.manifest.json")
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var manifest map[string]any
	if err = json.Unmarshal(b, &manifest); err != nil {
		t.Fatal(err)
	}
	manifest["models"].([]any)[0].(map[string]any)["input_price_per_million"] = 9
	put(t, path, manifest)
	if _, err = load(dir, testConfig); err == nil || !strings.Contains(err.Error(), "price") {
		t.Fatalf("expected price mismatch, got %v", err)
	}
}
