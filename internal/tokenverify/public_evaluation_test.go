package tokenverify

import (
	"context"
	"jevcancode/internal/tokenspec"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestPublicEvaluationCountsAllCases(t *testing.T) {
	p := problem(t, `{"intent":"identity","inputs":["a"],"allowed_types":["int"],"public_tests":[{"inputs":{"a":1},"want":2},{"inputs":{"a":2},"want":2}]}`)
	result, err := EvaluatePublic(context.Background(), p, "package main\nfunc F(a int) int { return a }", p.PublicTests)
	if err != nil || result.Status != "completed" || result.Passed != 1 || result.Total != 2 {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	bad, err := EvaluatePublic(context.Background(), p, "package main\nfunc F(a int) int { return unknown }", p.PublicTests)
	if err != nil || bad.Status != "typecheck_failed" || bad.Passed != 0 {
		t.Fatalf("bad=%+v err=%v", bad, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := EvaluatePublic(ctx, p, "", []tokenspec.Test{}); err == nil {
		t.Fatal("cancellation must be error")
	}
}

func TestPublicEvaluationFailureHasNoPartialReward(t *testing.T) {
	p := problem(t, `{"intent":"index","inputs":["xs"],"allowed_types":["[]int","int"],"public_tests":[{"inputs":{"xs":[1]},"want":1},{"inputs":{"xs":[]},"want":0}]}`)
	result, err := EvaluatePublic(context.Background(), p, "package main\nfunc F(xs []int) int { return xs[0] }", p.PublicTests)
	if err != nil || result.Status != "execution_failed" || result.Passed != 0 {
		t.Fatalf("result=%+v err=%v", result, err)
	}
}

// 2026-11-12: 별도 자식 프로세스의 PATH만 바꿔 병렬 테스트와 실제 툴체인을 격리한다.
func TestPublicEvaluationCompilerExitIsInfrastructureError(t *testing.T) {
	if os.Getenv("TESSARO_TEST_BROKEN_COMPILER") == "1" {
		p := problem(t, `{"intent":"identity","inputs":["a"],"allowed_types":["int"],"public_tests":[{"inputs":{"a":1},"want":1}]}`)
		result, err := EvaluatePublic(context.Background(), p, "package main\nfunc F(a int) int { return a }", p.PublicTests)
		if err == nil || result.Status != "compile_failed" || result.Passed != 0 || result.Timing.Compiled || strings.Contains(err.Error(), "diagnostic-marker") {
			t.Fatalf("compiler infrastructure error not propagated safely: result=%+v err=%v", result, err)
		}
		return
	}
	dir := t.TempDir()
	source := filepath.Join(dir, "fake_compiler.go")
	if err := os.WriteFile(source, []byte("package main\nimport \"os\"\nfunc main(){ os.Stderr.WriteString(\"diagnostic-marker\"); os.Exit(7) }\n"), 0600); err != nil {
		t.Fatal(err)
	}
	name := "go"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	build := exec.Command("go", "build", "-o", filepath.Join(dir, name), source)
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build fake compiler: %v: %s", err, output)
	}
	child := exec.Command(os.Args[0], "-test.run=^TestPublicEvaluationCompilerExitIsInfrastructureError$")
	child.Env = []string{"PATH=" + dir, "SYSTEMROOT=" + os.Getenv("SYSTEMROOT"), "TEMP=" + dir, "TMP=" + dir, "TESSARO_TEST_BROKEN_COMPILER=1"}
	if output, err := child.CombinedOutput(); err != nil {
		t.Fatalf("isolated evaluator test: %v: %s", err, output)
	}
}
