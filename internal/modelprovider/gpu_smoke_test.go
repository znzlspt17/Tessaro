package modelprovider

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"tessaro/internal/application"
	"tessaro/internal/compare"
	"tessaro/internal/rank"
	"tessaro/internal/runner"
)

// 2026-11-07: 평소 테스트는 GPU를 로드하지 않고 명시한 실측 실행에서만 실제 워커 복구를 확인한다.
func TestActualDeciderSmoke(t *testing.T) {
	if os.Getenv("TESSARO_GPU_SMOKE") != "1" {
		t.Skip("explicit GPU smoke only")
	}
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	p := New(Config{Python: filepath.Join(root, ".venv-rocm72", "Scripts", "python.exe"), Script: filepath.Join(root, "runtime", "local_worker.py"), ModelDir: filepath.Join(root, "runtime", "models", "decider-4b")})
	defer p.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()
	model := compare.Model{ID: "decider", Ranker: "local", Model: "Mapika/decider-4b"}
	r, err := p.Resolve(ctx, model)
	if err != nil {
		t.Fatalf("prepare: %v", err)
	}
	candidates := []rank.Candidate{{ID: "a", Code: "return n + 1"}, {ID: "b", Code: "return n - 1"}}
	scores, err := r.Rank(ctx, "Return the integer n incremented by one.", candidates)
	if err != nil {
		t.Fatalf("first request: %v", err)
	}
	if err = rank.Validate(candidates, scores); err != nil {
		t.Fatal(err)
	}
	requestCtx, requestCancel := context.WithCancel(ctx)
	timer := time.AfterFunc(50*time.Millisecond, requestCancel)
	_, err = r.Rank(requestCtx, "Return the integer n incremented by one.", candidates)
	timer.Stop()
	requestCancel()
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled request: %v", err)
	}
	r, err = p.Resolve(ctx, model)
	if err != nil {
		t.Fatalf("prepare after cancellation: %v", err)
	}
	scores, err = r.Rank(ctx, "Return the integer n incremented by one.", candidates)
	if err != nil {
		t.Fatalf("request after cancellation: %v", err)
	}
	if err = rank.Validate(candidates, scores); err != nil {
		t.Fatal(err)
	}
	m := application.New(application.Config{OutputDir: t.TempDir(), Resolve: p.Resolve})
	defer m.Close(context.Background())
	_, err = m.Start(application.Request{Kind: "search", Engine: "token", SpecPath: filepath.Join(root, "examples", "token", "specs", "loop-factorial.json"), ProblemID: "loop-factorial", Model: model, Options: runner.Request{MaxCalls: 1}})
	if err != nil {
		t.Fatal(err)
	}
	for {
		snapshot := m.Snapshot()
		if snapshot.Status != "running" && snapshot.Status != "cancelling" {
			if snapshot.Status != "completed" || len(snapshot.Results) != 1 || snapshot.Results[0].Result.Calls != 1 || snapshot.Results[0].Result.Status != "budget_exhausted" {
				t.Fatalf("actual application request did not complete: %s", snapshot.Status)
			}
			break
		}
		select {
		case <-m.Changed():
		case <-ctx.Done():
			_ = m.Cancel()
			t.Fatal("application smoke timed out")
		}
	}
	t.Log("actual GPU prepare, valid response, cancellation and restart passed")
}
