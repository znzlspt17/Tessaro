package compare

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
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
	return dir, Manifest{Engine:"ast",Models: []Model{{ID: "fake", Ranker: "fake"}}, Problems: []Problem{{ID: "identity", Spec: "spec.json", Heldout: "heldout.json"}}, Strategies: []string{"greedy", "strict-greedy"}, Repeats: 1, Seed: 42}
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
