package runner

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"jevcancode/internal/rank"
	"jevcancode/internal/tokenspec"
	"jevcancode/internal/tokensynth"
	"jevcancode/internal/tokenverify"
	grammardata "jevcancode/third_party/tree-sitter-go"
	"math"
	"runtime"
	"time"
)

// 2026-11-05: 토큰 합성의 공개 후보를 먼저 고정하고 보류 입력은 평가 단계에서만 읽는다.
func RunToken(ctx context.Context, p tokenspec.Problem, r rank.Ranker, q Request) (out Result) {
	defer out.sampleResources()
	if q.ReturnPolicy != "" {
		p.ReturnPolicy = q.ReturnPolicy
	}
	started := time.Now()
	out.Engine = "token"
	out.CandidateUnit = "go_lexical_token"
	out.GrammarRevision = grammardata.Revision
	out.Status = "execution_error"
	out.Strategy = q.Strategy
	out.ReturnPolicy = p.ReturnPolicy
	out.ValidationMode = p.ValidationMode
	out.Seed = q.Seed
	if q.Strategy == "phs" {
		out.Heuristic = "grammar_min_remaining_zero_fallback"
	}
	if out.Strategy == "" {
		out.Strategy = "beam"
		q.Strategy = "beam"
	}
	out.CompletionPolicy = completionPolicy(q.Strategy)
	out.Runtime = runtime.Version()
	out.GOOS = runtime.GOOS
	out.GOARCH = runtime.GOARCH
	out.PhaseMS = map[string]float64{}
	defer func() {
		out.LatencyMS = float64(time.Since(started)) / float64(time.Millisecond)
		if q.TokenTrace != nil {
			q.TokenTrace(tokensynth.Event{ID: "run", Reason: "ended:" + out.Status})
		}
	}()
	if p.Validate() != nil || r == nil || q.MaxTokens < 0 || q.MaxTokens > 1000000 || q.MaxCalls < 0 || q.MaxCalls > 1000000 || q.MaxCandidates < 0 || q.MaxCandidates > 1000000 || q.MaxLocals < 0 || q.MaxLocals > 63 || q.MaxStatements < 0 || q.MaxStatements > 128 || q.MaxNesting < 0 || q.MaxNesting > 8 || q.Timeout < 0 {
		out.Status = "input_error"
		return
	}
	for _, price := range []*float64{q.InputPrice, q.OutputPrice} {
		if price != nil && (*price < 0 || math.IsNaN(*price) || math.IsInf(*price, 0)) {
			out.Status = "input_error"
			return
		}
	}
	if q.Strategy != "beam" && q.Strategy != "greedy" && q.Strategy != "strict-greedy" && q.Strategy != "lds" && q.Strategy != "levin" && q.Strategy != "phs" && q.Strategy != "mcts" {
		out.Status = "input_error"
		return
	}
	if q.DecisionBatchSize < 0 || q.DecisionBatchSize > 256 || (q.DecisionBatchSize > 1 && q.Strategy != "lds" && q.Strategy != "levin" && q.Strategy != "phs") {
		out.Status = "input_error"
		return
	}
	if q.DecisionBatchSize == 0 {
		q.DecisionBatchSize = 1
	}
	out.DecisionBatchSize = q.DecisionBatchSize
	out.DecisionMode = "parent_local_single"
	if q.Strategy == "beam" || q.Strategy == "greedy" || q.Strategy == "strict-greedy" {
		out.DecisionMode = "legacy_joint"
	}
	if q.DecisionBatchSize > 1 {
		out.DecisionMode = "parent_local_batch_prefetch"
	}
	b := p.Budgets
	if q.MaxTokens == 0 {
		q.MaxTokens = b.MaxTokens
		if q.MaxTokens == 0 {
			q.MaxTokens = 256
		}
	}
	if q.MaxCalls == 0 {
		q.MaxCalls = b.MaxCalls
		if q.MaxCalls == 0 {
			q.MaxCalls = 128
		}
	}
	if q.MaxCandidates == 0 {
		q.MaxCandidates = b.MaxCandidates
		if q.MaxCandidates == 0 {
			q.MaxCandidates = 8192
		}
	}
	if !q.LocalLimitSet {
		q.MaxLocals = b.MaxLocals
		if q.MaxLocals == 0 && !b.MaxLocalsSet {
			q.MaxLocals = 8
		}
	}
	if q.MaxStatements == 0 {
		q.MaxStatements = b.MaxStatements
		if q.MaxStatements == 0 {
			q.MaxStatements = 32
		}
	}
	if q.MaxNesting == 0 {
		q.MaxNesting = b.MaxNesting
		if q.MaxNesting == 0 {
			q.MaxNesting = 4
		}
	}
	if q.Timeout == 0 {
		q.Timeout = time.Duration(b.TimeoutSeconds) * time.Second
		if q.Timeout == 0 {
			q.Timeout = 300 * time.Second
		}
	}
	if q.MaxLocals > 63 || q.MaxStatements > 128 || q.MaxNesting > 8 {
		out.Status = "input_error"
		return
	}
	out.EffectiveBudgets = map[string]any{"max_tokens": q.MaxTokens, "max_calls": q.MaxCalls, "max_candidates": q.MaxCandidates, "max_locals": q.MaxLocals, "max_statements": q.MaxStatements, "max_nesting": q.MaxNesting, "timeout_seconds": q.Timeout.Seconds(), "compile_timeout_seconds": 30, "execution_timeout_seconds": 2}
	p.Budgets.MaxTokens = q.MaxTokens
	p.Budgets.MaxCalls = q.MaxCalls
	p.Budgets.MaxCandidates = q.MaxCandidates
	p.Budgets.MaxLocals = q.MaxLocals
	p.Budgets.MaxLocalsSet = true
	p.Budgets.MaxStatements = q.MaxStatements
	p.Budgets.MaxNesting = q.MaxNesting
	payload, _ := json.Marshal(p)
	digest := sha256.Sum256(payload)
	out.InputHash = hex.EncodeToString(digest[:])
	ctx, cancel := context.WithTimeout(ctx, q.Timeout)
	defer cancel()
	m := &measuredRanker{Ranker: r, inputKnown: true, outputKnown: true}
	if q.DecisionBatchSize > 1 && !m.SupportsDecisionBatch() {
		out.DecisionMode = "parent_local_sequential_fallback"
	}
	var requestCounter rank.HTTPRequestCounter
	var initialRequests int64
	if counter, ok := r.(rank.HTTPRequestCounter); ok {
		requestCounter = counter
		initialRequests = counter.HTTPRequests()
	}
	defer func() {
		if requestCounter != nil {
			count := requestCounter.HTTPRequests() - initialRequests
			out.HTTPRequests = &count
		}
		out.PhaseMS["rank"] = float64(m.elapsed) / float64(time.Millisecond)
		out.recordUsage(m, q.InputPrice, q.OutputPrice)
		if provider, ok := r.(rank.MetadataProvider); ok {
			out.Ranker = provider.Metadata()
		}
	}()
	var trace func(tokensynth.Event)
	verificationID := "verification"
	if q.TokenTrace != nil {
		trace = func(e tokensynth.Event) {
			if (e.Reason == "decision_context" || e.Reason == "prefetch_context") && !q.TraceCode && !q.TraceDecisionContext {
				return
			}
			if e.Reason == "validation_started" {
				verificationID = e.ID
			}
			if !q.TraceCode {
				e.Code = ""
			}
			q.TokenTrace(e)
		}
	}
	phase := func(name, status string) {
		if trace == nil {
			return
		}
		reason := "phase_finished"
		if status == "started" {
			reason = "phase_started"
		}
		trace(tokensynth.Event{ID: verificationID, Reason: reason, Phase: name, Status: status})
	}
	var selected *tokenverify.Binding
	searchStart := time.Now()
	s, err := tokensynth.Search(ctx, p, m, tokensynth.Options{Strategy: q.Strategy, Seed: q.Seed, DecisionBatchSize: q.DecisionBatchSize, MaxTokens: q.MaxTokens, MaxCalls: q.MaxCalls, MaxCandidates: q.MaxCandidates, Trace: trace, TraceCode: q.TraceCode, PublicEvaluate: func(ctx context.Context, source string) (tokenverify.PublicEvaluation, error) {
		t := time.Now()
		evaluation, err := tokenverify.EvaluatePublic(ctx, p, source, p.PublicTests)
		out.PhaseMS["public_verify"] += float64(time.Since(t)) / float64(time.Millisecond)
		out.PhaseMS["compile"] += float64(evaluation.Timing.Compile) / float64(time.Millisecond)
		out.PhaseMS["public_execute"] += float64(evaluation.Timing.Execute) / float64(time.Millisecond)
		if evaluation.Status == "typecheck_failed" {
			out.TypecheckFailures++
		}
		if evaluation.Timing.Compile > 0 {
			out.CompileAttempts++
		}
		if evaluation.Timing.Compiled {
			out.CompilePasses++
		}
		return evaluation, err
	}}, func(source string) (bool, error) {
		t := time.Now()
		phase("typecheck", "started")
		binding, e := tokenverify.Validate(p, source)
		out.PhaseMS["typecheck"] += float64(time.Since(t)) / float64(time.Millisecond)
		if e != nil {
			phase("typecheck", "failed")
			out.TypecheckFailures++
			return false, nil
		}
		phase("typecheck", "passed")
		t = time.Now()
		passed, timing, e := tokenverify.CheckObserved(ctx, p, binding.Source, p.PublicTests, func(name, status string) {
			if name == "execute" {
				name = "public"
			}
			phase(name, status)
		})
		out.PhaseMS["public_verify"] += float64(time.Since(t)) / float64(time.Millisecond)
		out.PhaseMS["compile"] += float64(timing.Compile) / float64(time.Millisecond)
		out.PhaseMS["public_execute"] += float64(timing.Execute) / float64(time.Millisecond)
		if timing.Compile > 0 {
			out.CompileAttempts++
		}
		if timing.Compiled {
			out.CompilePasses++
		}
		if errors.Is(e, tokenverify.ErrCompileTimeout) || errors.Is(e, tokenverify.ErrExecutionTimeout) || errors.Is(e, tokenverify.ErrCompileFailed) {
			return false, nil
		}
		if e != nil {
			return false, e
		}
		if passed {
			selected = binding
		}
		return passed, nil
	})
	out.PhaseMS["generation"] = float64(time.Since(searchStart))/float64(time.Millisecond) - float64(m.elapsed)/float64(time.Millisecond) - out.PhaseMS["typecheck"] - out.PhaseMS["public_verify"]
	if out.PhaseMS["generation"] < 0 {
		out.PhaseMS["generation"] = 0
	}
	out.Candidates = s.Candidates
	out.Calls = s.Calls
	out.Questions = m.calls
	out.Expansions = s.Expansions
	out.MemoHits = s.MemoHits
	out.EvaluationMemoHits = s.EvaluationMemoHits
	out.Validations = s.Validations
	out.CompileFailures = out.CompileAttempts - out.CompilePasses
	out.TokenCount = len(s.Tokens)
	out.GeneratedTokens = s.GeneratedTokens
	out.ForcedSteps = s.ForcedSteps
	out.ModelSteps = s.ModelSteps
	out.ForcedTokens = s.ForcedTokens
	out.ModelSelectedTokens = s.ModelSelectedTokens
	out.EmittedTokens = append([]string(nil), s.Tokens...)
	if err != nil {
		var modelErr modelFailure
		var coded interface{ ErrorCode() string }
		if errors.As(err, &coded) {
			out.ErrorCode = coded.ErrorCode()
		}
		switch {
		case out.ErrorCode == "question_budget_exhausted":
			out.Status = "budget_exhausted"
		case errors.Is(err, tokensynth.ErrBudget):
			out.Status = "budget_exhausted"
		case errors.Is(err, tokensynth.ErrCandidateLimit):
			out.Status = "candidate_limit"
		case errors.Is(err, tokensynth.ErrNoSolution):
			out.Status = "no_solution"
		case errors.Is(err, context.DeadlineExceeded):
			out.Status = "timeout"
		case errors.Is(err, context.Canceled):
			out.Status = "cancelled"
		case errors.As(err, &modelErr):
			out.Status = "model_error"
		}
		return
	}
	if selected == nil {
		return
	}
	out.Source = selected.Source
	out.UsedLocals = selected.Locals
	out.PublicPassed = true
	out.PublicPassCount = 1
	out.Status = "success"
	if q.TokenHeldout != nil {
		t := time.Now()
		phase("heldout", "started")
		tests, e := q.TokenHeldout()
		if e == nil {
			data, _ := json.Marshal(tests)
			hash := sha256.Sum256(data)
			out.HeldoutHash = hex.EncodeToString(hash[:])
			var passed bool
			var timing tokenverify.Timing
			passed, timing, e = tokenverify.Check(ctx, p, selected.Source, tests)
			out.PhaseMS["heldout_compile"] = float64(timing.Compile) / float64(time.Millisecond)
			out.PhaseMS["heldout_execute"] = float64(timing.Execute) / float64(time.Millisecond)
			if e == nil {
				out.HeldoutPassed = &passed
				if !passed {
					out.Status = "heldout_failed"
				}
			}
		}
		out.PhaseMS["heldout"] = float64(time.Since(t)) / float64(time.Millisecond)
		heldoutStatus := "failed"
		if e == nil && out.HeldoutPassed != nil && *out.HeldoutPassed {
			heldoutStatus = "passed"
		}
		phase("heldout", heldoutStatus)
		if e != nil {
			switch {
			case errors.Is(e, context.Canceled):
				out.Status = "cancelled"
			case errors.Is(e, context.DeadlineExceeded):
				out.Status = "timeout"
			case errors.Is(e, tokenverify.ErrCompileTimeout), errors.Is(e, tokenverify.ErrExecutionTimeout):
				out.Status = "heldout_timeout"
			default:
				out.Status = "heldout_error"
			}
		}
	}
	return
}
