package verify

import (
	"context"
	"encoding/json"
	"jevcancode/internal/benchmark"
	"jevcancode/internal/spec"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// 2026-11-05: 정답 ID만 검사하지 않고 실제 Go 실행으로 의미적 오답과 복수 정답의 라벨을 확인한다.
func TestExpandedChoiceLabelsAgainstExecution(t *testing.T) {
	base := filepath.Join("..", "..", "examples")
	f, err := os.Open(filepath.Join(base, "benchmark-expanded.json"))
	if err != nil {
		t.Fatal(err)
	}
	manifest, err := benchmark.Decode(f)
	f.Close()
	if err != nil || len(manifest.Problems) != 24 {
		t.Fatalf("choice manifest: count=%d error=%v", len(manifest.Problems), err)
	}
	data, err := os.ReadFile(filepath.Join(base, "suite", "references.json"))
	if err != nil {
		t.Fatal(err)
	}
	var references []struct{ ID, Spec, Heldout string }
	if err := json.Unmarshal(data, &references); err != nil {
		t.Fatal(err)
	}
	paths := map[string]struct{ spec, heldout string }{}
	for _, ref := range references {
		paths[ref.ID] = struct{ spec, heldout string }{ref.Spec, ref.Heldout}
	}
	for _, problem := range manifest.Problems {
		t.Run(problem.ID, func(t *testing.T) {
			path, ok := paths[problem.ID]
			if !ok {
				t.Fatal("missing synthesis reference")
			}
			f, err := os.Open(filepath.Join(base, "suite", path.spec))
			if err != nil {
				t.Fatal(err)
			}
			p, err := spec.Decode(f)
			f.Close()
			if err != nil {
				t.Fatal(err)
			}
			f, err = os.Open(filepath.Join(base, "suite", path.heldout))
			if err != nil {
				t.Fatal(err)
			}
			heldout, err := spec.DecodeTests(f, p)
			f.Close()
			if err != nil {
				t.Fatal(err)
			}
			tests := append(append([]spec.Test(nil), p.PublicTests...), heldout...)
			answers := map[string]bool{}
			for _, id := range problem.Answers {
				answers[id] = true
			}
			for _, candidate := range problem.Candidates {
				ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
				passed, err := Check(ctx, p, candidate.Code, tests)
				cancel()
				if err != nil || passed != answers[candidate.ID] {
					t.Errorf("candidate %s: passed=%v expected=%v error=%v", candidate.ID, passed, answers[candidate.ID], err)
				}
			}
		})
	}
}
