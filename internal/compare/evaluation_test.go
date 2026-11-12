package compare

import (
	"context"
	"encoding/json"
	"jevcancode/internal/rank"
	"jevcancode/internal/runner"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func evaluationFixture(t *testing.T) (string, Manifest) {
	t.Helper()
	dir := t.TempDir()
	problem := `{"intent":"identity","name":"Identity","params":[{"name":"x","type":"int"}],"return_type":"int","constants":[0],"public_tests":[{"args":[1],"want":1}]}`
	if err := os.WriteFile(filepath.Join(dir, "spec.json"), []byte(problem), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "heldout.json"), []byte(`[{"args":[2],"want":2}]`), 0600); err != nil {
		t.Fatal(err)
	}
	return dir, Manifest{Engine: "ast", Models: []Model{{ID: "fake", Ranker: "fake"}}, Problems: []Problem{{ID: "identity", Spec: "spec.json", Heldout: "heldout.json"}}, Strategies: []string{"greedy", "strict-greedy"}, Repeats: 1, Seed: 42}
}

func TestResolveCancellationIsNotReportedAsModelError(t *testing.T) {
	dir, manifest := evaluationFixture(t)
	manifest.Strategies = []string{"greedy"}
	ctx, cancel := context.WithCancel(context.Background())
	report := RunWithResolver(ctx, dir, manifest, nil, func(context.Context, Model) (rank.Ranker, error) { cancel(); return nil, context.Canceled })
	if len(report.Outcomes) != 1 || report.Outcomes[0].Result.Status != "cancelled" || report.Outcomes[0].Error != "" {
		t.Fatalf("outcomes=%+v", report.Outcomes)
	}
}
func TestResolveDeadlineIsNotReportedAsModelError(t *testing.T) {
	dir, manifest := evaluationFixture(t)
	manifest.Strategies = []string{"greedy"}
	report := RunWithResolver(context.Background(), dir, manifest, nil, func(context.Context, Model) (rank.Ranker, error) { return nil, context.DeadlineExceeded })
	if len(report.Outcomes) != 1 || report.Outcomes[0].Result.Status != "timeout" || report.Outcomes[0].Error != "" {
		t.Fatalf("outcomes=%+v", report.Outcomes)
	}
}

func TestEvaluationSnapshotIsSharedAcrossTrials(t *testing.T) {
	dir, manifest := evaluationFixture(t)
	changed := false
	report := Run(context.Background(), dir, manifest, func(value any) {
		data, _ := json.Marshal(value)
		var event struct {
			TrialIndex int `json:"trial_index"`
		}
		if err := json.Unmarshal(data, &event); err != nil {
			t.Fatal(err)
		}
		if event.TrialIndex == 2 && !changed {
			changed = true
			if err := os.WriteFile(filepath.Join(dir, "heldout.json"), []byte(`[{"args":[2],"want":99}]`), 0600); err != nil {
				t.Fatal(err)
			}
		}
	})
	if !changed || report.Successful != 2 {
		t.Fatalf("evaluation changed between trials: changed=%v successful=%d", changed, report.Successful)
	}
}

func TestConfigurationHashIncludesLoadedEvaluation(t *testing.T) {
	dir, manifest := evaluationFixture(t)
	manifest.Strategies = []string{"strict-greedy"}
	first := Run(context.Background(), dir, manifest, nil)
	if err := os.WriteFile(filepath.Join(dir, "heldout.json"), []byte(`[{"args":[3],"want":3}]`), 0600); err != nil {
		t.Fatal(err)
	}
	second := Run(context.Background(), dir, manifest, nil)
	if first.Successful != 1 || second.Successful != 1 || first.ConfigHash == second.ConfigHash {
		t.Fatalf("configuration does not identify evaluation: first=%+v second=%+v", first, second)
	}
}

func TestCancellationDuringTrialPreservesPartialCounts(t *testing.T) {
	dir, manifest := evaluationFixture(t)
	manifest.Strategies = []string{"greedy", "beam", "strict-greedy"}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	report := Run(ctx, dir, manifest, func(any) { cancel() })
	if report.Total != 3 || report.Attempted != 1 || report.Failed != 1 || report.NotRunCancelled != 2 || report.Outcomes[0].Result.Status != "cancelled" {
		t.Fatalf("partial counts: %+v", report)
	}
	if report.HeldoutHashes["identity"] != "not_loaded" {
		t.Fatal("unsubmitted evaluation was loaded")
	}
	for _, group := range report.Groups {
		if group.Attempted == 0 && (group.SuccessRate != nil || group.AllAttemptMeanMS != nil) {
			t.Fatal("unattempted group was assigned a success rate or latency")
		}
	}
}

func TestRunWithOptionsLoadsAbsoluteSpecAndHeldoutPaths(t *testing.T) {
	dir, m := evaluationFixture(t)
	m.Strategies = []string{"strict-greedy"}
	m.Problems[0].Spec = filepath.Join(dir, "spec.json")
	m.Problems[0].Heldout = filepath.Join(dir, "heldout.json")
	report := RunWithOptions(context.Background(), t.TempDir(), m, nil, func(context.Context, Model) (rank.Ranker, error) { return rank.Fake{}, nil }, runner.Request{})
	if len(report.Outcomes) != 1 || report.Outcomes[0].Result.Status != "success" || report.Outcomes[0].Result.HeldoutPassed == nil || !*report.Outcomes[0].Result.HeldoutPassed || report.HeldoutHashes["identity"] == "unavailable" {
		t.Fatalf("absolute paths not loaded: %+v", report.Outcomes)
	}
}
func TestUnavailableInputHasSafeDistinctErrorCode(t *testing.T) {
	m := Manifest{Engine: "token", Models: []Model{{ID: "f", Ranker: "fake"}}, Problems: []Problem{{ID: "missing", Spec: "missing.json"}}, Strategies: []string{"beam"}, Repeats: 1}
	report := Run(context.Background(), t.TempDir(), m, nil)
	out := report.Outcomes[0]
	if out.Result.Status != "input_error" || out.Result.ErrorCode != "input_unavailable" || out.Error != "input_unavailable" {
		t.Fatalf("wrong missing input diagnosis: %+v", out)
	}
}

type tokenScriptedRanker struct{ privateSeen bool }

func (r *tokenScriptedRanker) Rank(_ context.Context, state string, cs []rank.Candidate) ([]rank.Score, error) {
	if strings.Contains(state, "1007") {
		r.privateSeen = true
	}
	var context struct {
		Parents map[string]struct {
			Prefix string `json:"prefix"`
		} `json:"parents"`
	}
	if err := json.Unmarshal([]byte(state), &context); err != nil {
		return nil, err
	}
	target := "package main ; func f0 ( x int ) int { return x ; }"
	scores := make([]rank.Score, len(cs))
	for i, c := range cs {
		score := 0.0
		prefix := strings.TrimSpace(context.Parents[c.ParentID].Prefix + " " + c.Lexeme)
		if strings.HasPrefix(target, prefix) {
			score = 1
		}
		scores[i] = rank.Score{ID: c.ID, Score: score}
	}
	return scores, nil
}
func TestTokenRunWithOptionsLoadsAbsoluteSpecAndHeldoutPaths(t *testing.T) {
	dir := t.TempDir()
	specPath := filepath.Join(dir, "spec.json")
	heldoutPath := filepath.Join(dir, "heldout.json")
	if err := os.WriteFile(specPath, []byte(`{"intent":"identity","inputs":["x"],"allowed_types":["int"],"public_tests":[{"inputs":{"x":1},"want":1}]}`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(heldoutPath, []byte(`[{"inputs":{"x":1007},"want":1007}]`), 0600); err != nil {
		t.Fatal(err)
	}
	m := Manifest{Engine: "token", Models: []Model{{ID: "f", Ranker: "fake"}}, Problems: []Problem{{ID: "p", Spec: specPath, Heldout: heldoutPath}}, Strategies: []string{"strict-greedy"}, Repeats: 1}
	ranker := &tokenScriptedRanker{}
	report := RunWithOptions(context.Background(), t.TempDir(), m, nil, func(context.Context, Model) (rank.Ranker, error) { return ranker, nil }, runner.Request{})
	if len(report.Outcomes) != 1 || report.Outcomes[0].Result.Status != "success" || report.Outcomes[0].Result.HeldoutPassed == nil || !*report.Outcomes[0].Result.HeldoutPassed || ranker.privateSeen {
		t.Fatalf("absolute token paths or privacy failed: %+v private=%v", report.Outcomes, ranker.privateSeen)
	}
}
