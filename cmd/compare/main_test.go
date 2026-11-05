package main

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestFakeTokenComparisonSavesPartialResults(t *testing.T) {
	dir := t.TempDir()
	exe := filepath.Join(dir, "compare.exe")
	build := exec.Command("go", "build", "-o", exe, ".")
	if b, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build: %s %v", b, err)
	}
	spec := `{"intent":"identity","inputs":["x"],"allowed_types":["int"],"public_tests":[{"inputs":{"x":1},"want":1}],"budgets":{"max_candidates":1}}`
	manifest := `{"models":[{"id":"fake","ranker":"fake"}],"problems":[{"id":"p","spec":"p.json","heldout":"missing.json"}],"strategies":["beam"]}`
	for name, data := range map[string]string{"p.json": spec, "compare.json": manifest} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
	}
	cmd := exec.Command(exe, "-manifest", filepath.Join(dir, "compare.json"), "-trace")
	cmd.Dir = dir
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("run %s: %v", stderr.String(), err)
	}
	var result struct {
		Engine    string            `json:"engine"`
		Attempted int               `json:"attempted"`
		Heldout   map[string]string `json:"heldout_hashes"`
		Outcomes  []struct {
			Result struct{ Status, Engine string } `json:"result"`
		} `json:"outcomes"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result.Engine != "token" || result.Attempted != 1 || result.Heldout["p"] != "not_loaded" || len(result.Outcomes) != 1 || result.Outcomes[0].Result.Status != "budget_exhausted" || result.Outcomes[0].Result.Engine != "token" {
		t.Fatalf("%+v", result)
	}
	entries, err := os.ReadDir(filepath.Join(dir, "output"))
	if err != nil || len(entries) != 2 {
		t.Fatalf("%v %v", entries, err)
	}
	var saved, trace []byte
	for _, e := range entries {
		data, err := os.ReadFile(filepath.Join(dir, "output", e.Name()))
		if err != nil {
			t.Fatal(err)
		}
		if strings.HasSuffix(e.Name(), ".trace.jsonl") {
			trace = data
		} else {
			saved = data
		}
	}
	if !bytes.Equal(saved, stdout.Bytes()) || !bytes.Contains(trace, []byte(`"lexeme":"package"`)) {
		t.Fatal("saved result or trace differs")
	}
}
