package compare

import (
	"context"
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
	if r.Outcomes[0].Result.Status != "input_error" || !strings.Contains(r.Outcomes[0].Error, "engine ast") {
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
