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

func TestCLIRequiresExplicitASTForLegacy(t *testing.T) {
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	exe := filepath.Join(dir, "synth.exe")
	build := exec.Command("go", "build", "-o", exe, ".")
	if out, e := build.CombinedOutput(); e != nil {
		t.Fatalf("build %s: %v", out, e)
	}
	spec := filepath.Join(root, "examples", "suite", "specs", "arithmetic-add.json")
	invoke := func(args ...string) (map[string]any, string, error) {
		cmd := exec.Command(exe, args...)
		cmd.Dir = dir
		var stdout, stderr bytes.Buffer
		cmd.Stdout = &stdout
		cmd.Stderr = &stderr
		e := cmd.Run()
		var result map[string]any
		if err := json.Unmarshal(stdout.Bytes(), &result); err != nil {
			t.Fatalf("invalid JSON %q: %v", stdout.String(), err)
		}
		return result, stderr.String(), e
	}
	got, stderr, e := invoke("-spec", spec)
	if e == nil || got["status"] != "input_or_execution_error" || !strings.Contains(stderr, "-engine ast") {
		t.Fatalf("default: %+v %q %v", got, stderr, e)
	}
	got, _, e = invoke("-spec", spec, "-engine", "ast", "-max-candidates", "1")
	if e == nil || got["engine"] != "ast" || got["strategy"] != "greedy" || got["status"] != "budget_exhausted" {
		t.Fatalf("ast: %+v %v", got, e)
	}
	if _, e = os.Stat(filepath.Join(dir, "output")); e != nil {
		t.Fatal(e)
	}
}
func TestCLITokenDefaultsAndSavedTrace(t *testing.T) {
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	exe := filepath.Join(dir, "synth.exe")
	build := exec.Command("go", "build", "-o", exe, ".")
	if out, e := build.CombinedOutput(); e != nil {
		t.Fatalf("build %s: %v", out, e)
	}
	cmd := exec.Command(exe, "-spec", filepath.Join(root, "examples", "token", "specs", "identity.json"), "-max-candidates", "1", "-max-locals", "0", "-trace")
	cmd.Dir = dir
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err = cmd.Run()
	if err == nil {
		t.Fatal("expected budget exhaustion")
	}
	var result struct {
		Engine, Strategy, Status string
		EffectiveBudgets         map[string]any `json:"effective_budgets"`
	}
	if err = json.Unmarshal(stdout.Bytes(), &result); err != nil {
		t.Fatalf("%q: %v", stdout.String(), err)
	}
	if result.Engine != "token" || result.Strategy != "beam" || result.Status != "budget_exhausted" || result.EffectiveBudgets["max_locals"] != float64(0) || result.EffectiveBudgets["max_tokens"] != float64(256) || result.EffectiveBudgets["max_calls"] != float64(128) {
		t.Fatalf("%+v %q", result, stderr.String())
	}
	files, err := os.ReadDir(filepath.Join(dir, "output"))
	if err != nil || len(files) != 2 {
		t.Fatalf("output files %v %v", files, err)
	}
	var saved, trace []byte
	for _, f := range files {
		data, e := os.ReadFile(filepath.Join(dir, "output", f.Name()))
		if e != nil {
			t.Fatal(e)
		}
		if strings.HasSuffix(f.Name(), ".trace.jsonl") {
			trace = data
		} else {
			saved = data
		}
	}
	if !bytes.Equal(saved, stdout.Bytes()) || !bytes.Contains(trace, []byte(`"lexeme":"package"`)) || bytes.Contains(trace, []byte(`"code":`)) {
		t.Fatalf("bad saved output or trace: %s", trace)
	}
}
