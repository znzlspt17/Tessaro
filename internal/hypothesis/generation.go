package hypothesis

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"tessaro/internal/rank"
	"tessaro/internal/runner"
	"tessaro/internal/tokenspec"
	"tessaro/internal/tokensynth"
	"math/rand"
	"strings"
	"time"
)

type GenerationResult struct {
	ProblemID         string         `json:"problem_id"`
	Strategy          string         `json:"strategy"`
	Ranker            string         `json:"ranker"`
	Status            string         `json:"status"`
	Seed              *int64         `json:"seed,omitempty"`
	Calls             int            `json:"calls"`
	Candidates        int            `json:"candidates"`
	GeneratedTokens   int            `json:"generated_tokens"`
	ForcedSteps       int            `json:"forced_steps"`
	ModelSteps        int            `json:"model_steps"`
	CompileAttempts   int            `json:"compile_attempts"`
	CompilePasses     int            `json:"compile_passes"`
	CompileFailures   int            `json:"compile_failures"`
	TypecheckFailures int            `json:"typecheck_failures"`
	Source            string         `json:"source,omitempty"`
	ValidationMode    string         `json:"validation_mode"`
	ActualModel       string         `json:"actual_model,omitempty"`
	Usage             *rank.Usage    `json:"usage,omitempty"`
	PublicPassed      bool           `json:"public_passed"`
	HeldoutPassed     *bool          `json:"heldout_passed,omitempty"`
	HeldoutHash       string         `json:"heldout_hash,omitempty"`
	EffectiveBudgets  map[string]any `json:"effective_budgets,omitempty"`
}
type permutationRanker struct {
	rng     *rand.Rand
	observe func(string, string, []rank.Candidate, rank.Decision)
}

type heldoutSnapshot struct {
	tests []tokenspec.Test
	hash  string
	err   error
}

// 2026-11-08: 최초 공개 통과 시에만 보류 집합을 고정해 비교군 사이의 파일 변경 영향을 차단한다.
func loadHeldout(cache map[string]heldoutSnapshot, p frozenProblem) heldoutSnapshot {
	if v, ok := cache[p.id]; ok {
		return v
	}
	var result heldoutSnapshot
	b, err := readPublicInput(p.heldoutPath)
	if err == nil {
		h := sha256.Sum256(b)
		result.hash = hex.EncodeToString(h[:])
		result.tests, err = tokenspec.DecodeTests(strings.NewReader(string(b)), p.problem)
	}
	result.err = err
	cache[p.id] = result
	return result
}

func (r *permutationRanker) Rank(_ context.Context, state string, cs []rank.Candidate) ([]rank.Score, error) {
	if r.observe != nil {
		r.observe("request", state, cs, rank.Decision{})
	}
	order := r.rng.Perm(len(cs))
	out := make([]rank.Score, len(cs))
	for i, c := range cs {
		out[i] = rank.Score{ID: c.ID, Score: float64(order[i]) / float64(len(cs))}
	}
	if r.observe != nil {
		r.observe("decision", state, cs, rank.Decision{Scores: out})
	}
	return out, nil
}

// 2026-11-08: 동일한 생성 예산을 모델과 무작위 순열에 적용하며 보류 데이터는 공개 검증 성공 뒤에만 연다.
func runGeneration(ctx context.Context, problems []frozenProblem, model rank.Ranker, limits budgets, progress func(Progress)) ([]GenerationResult, error) {
	type job struct {
		p              frozenProblem
		strategy, kind string
		seed           *int64
	}
	jobs := make([]job, 0, len(problems)*8)
	for _, p := range problems {
		for _, strategy := range []string{"strict-greedy", "beam"} {
			jobs = append(jobs, job{p: p, strategy: strategy, kind: "jev"})
			for _, seed := range []int64{42, 43, 44} {
				s := seed
				jobs = append(jobs, job{p: p, strategy: strategy, kind: "random", seed: &s})
			}
		}
	}
	out := make([]GenerationResult, len(jobs))
	for i, j := range jobs {
		out[i] = GenerationResult{ProblemID: j.p.id, Strategy: j.strategy, Ranker: j.kind, Seed: j.seed, Status: "not_started", ValidationMode: j.p.problem.ValidationMode}
	}
	if model == nil || limits.MaxCalls <= 0 || limits.MaxTokens <= 0 || limits.MaxCandidates <= 0 || limits.TimeoutSeconds <= 0 {
		return out, errors.New("invalid_input")
	}
	var stop error
	heldouts := map[string]heldoutSnapshot{}
	for i, j := range jobs {
		if stop != nil {
			break
		}
		if ctx.Err() != nil {
			stop = ctx.Err()
			break
		}
		var r rank.Ranker = model
		if j.seed != nil {
			r = &permutationRanker{rng: rand.New(rand.NewSource(*j.seed))}
		}
		g, _ := model.(*guardedRanker)
		emit := func(event *DetailEvent, completed int) {
			if progress != nil {
				p := Progress{Phase: "generation", ProblemID: j.p.id, Completed: completed, Total: len(jobs), Event: event}
				if g != nil {
					p.RequestsUsed = g.used
					p.RequestLimit = g.limit
				}
				progress(p)
			}
		}
		step := 0
		observe := func(kind, state string, candidates []rank.Candidate, decision rank.Decision) {
			event := &DetailEvent{Phase: "generation", ProblemID: j.p.id, Kind: j.kind + ":" + j.strategy + ":" + kind, Seed: j.seed, Step: step}
			if kind == "request" {
				event.State, event.Candidates = state, candidates
			} else {
				event.Scores, event.ChosenID, event.Usage = decision.Scores, decision.ChosenID, decision.Usage
			}
			emit(event, i)
		}
		if j.kind == "jev" && g != nil {
			g.observe = observe
		}
		if random, ok := r.(*permutationRanker); ok {
			random.observe = observe
		}
		emit(nil, i)
		type branch struct {
			position int
			matches  bool
		}
		paths := map[string]branch{"root": {matches: true}}
		referenceTokens := normalize(j.p.reference.Source, j.p.problem)
		request := runner.Request{Strategy: j.strategy, MaxCalls: limits.MaxCalls, MaxTokens: limits.MaxTokens, MaxCandidates: limits.MaxCandidates, Timeout: time.Duration(limits.TimeoutSeconds) * time.Second,
			TokenTrace: func(e tokensynth.Event) {
				if e.Step > 0 {
					step = e.Step
				}
				if (e.Reason == "active" || e.Reason == "forced" || e.Reason == "public_passed" || e.Reason == "public_failed") && e.Lexeme != "" {
					parent, ok := paths[e.ParentID]
					matches := ok && parent.matches && parent.position < len(referenceTokens) && referenceTokens[parent.position] == e.Lexeme
					paths[e.ID] = branch{position: parent.position + 1, matches: matches}
					status := "unknown"
					if matches {
						status = "reference_prefix"
					}
					emit(&DetailEvent{Phase: "generation", ProblemID: j.p.id, Kind: j.kind + ":" + j.strategy, Seed: j.seed, Step: e.Step, ID: e.ID, ParentID: e.ParentID, Lexeme: e.Lexeme, Reason: e.Reason, Rank: e.Rank, BranchStatus: status}, i)
				} else if (e.Reason == "public_passed" || e.Reason == "public_failed") && e.ID != "" {
					status := "unknown"
					if p, ok := paths[e.ID]; ok && p.matches {
						status = "reference_prefix"
					}
					emit(&DetailEvent{Phase: "generation", ProblemID: j.p.id, Kind: j.kind + ":" + j.strategy, Seed: j.seed, Step: e.Step, ID: e.ID, Reason: e.Reason, BranchStatus: status}, i)
				} else if e.Reason == "decision_result" {
					emit(&DetailEvent{Phase: "generation", ProblemID: j.p.id, Kind: j.kind + ":" + j.strategy, Seed: j.seed, Step: e.Step, Reason: e.Reason, ChosenID: e.ChosenID}, i)
				}
			},
		}
		if j.p.problem.ValidationMode == "sampled_holdout" || (j.p.heldoutPath != "" && j.p.problem.ValidationMode != "exhaustive_public") {
			request.TokenHeldout = func() ([]tokenspec.Test, error) {
				snapshot := loadHeldout(heldouts, j.p)
				out[i].HeldoutHash = snapshot.hash
				return snapshot.tests, snapshot.err
			}
		}
		result := runner.RunToken(ctx, j.p.problem, r, request)
		if j.kind == "jev" && g != nil {
			g.observe = nil
		}
		x := &out[i]
		x.Status = result.Status
		x.Calls = result.Calls
		x.Candidates = result.Candidates
		x.GeneratedTokens = result.GeneratedTokens
		x.ForcedSteps = result.ForcedSteps
		x.ModelSteps = result.ModelSteps
		x.CompileAttempts = result.CompileAttempts
		x.CompilePasses = result.CompilePasses
		x.CompileFailures = result.CompileFailures
		x.TypecheckFailures = result.TypecheckFailures
		x.Source, x.Usage = result.Source, result.Usage
		if g != nil && j.kind == "jev" {
			x.ActualModel = g.actualModel
		}
		x.PublicPassed = result.PublicPassed
		x.HeldoutPassed = result.HeldoutPassed
		x.EffectiveBudgets = result.EffectiveBudgets
		if ctx.Err() != nil {
			stop = ctx.Err()
			if errors.Is(stop, context.DeadlineExceeded) {
				x.Status = "timeout"
			} else {
				x.Status = "cancelled"
			}
		} else if g != nil && g.failure != nil {
			if errors.Is(g.failure, errRequestBudget) {
				x.Status = "budget_exhausted"
				stop = errRequestBudget
			} else if errors.Is(g.failure, errModelMismatch) {
				x.Status = "model_mismatch"
				stop = errModelMismatch
			} else {
				x.Status = "service_error"
				stop = errService
			}
		} else if result.Status == "model_error" {
			x.Status = "service_error"
			stop = errService
		}
		emit(nil, i+1)
	}
	return out, stop
}
