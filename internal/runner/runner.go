package runner

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"jevcancode/internal/rank"
	"jevcancode/internal/spec"
	"jevcancode/internal/synth"
	"jevcancode/internal/tokenspec"
	"jevcancode/internal/tokensynth"
	"jevcancode/internal/verify"
	"math"
	"runtime"
	"time"
)

type Request struct {
	MaxTokens, MaxNesting                                       int
	TokenHeldout                                                func() ([]tokenspec.Test, error)
	TokenTrace                                                  func(tokensynth.Event)
	Strategy                                                    string
	MaxCandidates, MaxCalls, MaxDepth, MaxLocals, MaxStatements int
	LocalLimitSet                                               bool
	Timeout                                                     time.Duration
	Heldout                                                     func() ([]spec.Test, error)
	Trace                                                       func(synth.TraceEvent)
	TraceCode                                                   bool
	TraceDecisionContext                                        bool
	InputPrice, OutputPrice                                     *float64
}
type Result struct {
	Engine              string             `json:"engine"`
	CandidateUnit       string             `json:"candidate_unit"`
	GrammarRevision     string             `json:"grammar_revision,omitempty"`
	TokenCount          int                `json:"token_count,omitempty"`
	GeneratedTokens     int                `json:"generated_token_count,omitempty"`
	ForcedSteps         int                `json:"forced_step_count,omitempty"`
	ModelSteps          int                `json:"model_step_count,omitempty"`
	ForcedTokens        int                `json:"forced_tokens,omitempty"`
	ModelSelectedTokens int                `json:"model_selected_tokens,omitempty"`
	EmittedTokens       []string           `json:"emitted_tokens,omitempty"`
	Status              string             `json:"status"`
	Source              string             `json:"source,omitempty"`
	PublicPassed        bool               `json:"public_passed"`
	HeldoutPassed       *bool              `json:"heldout_passed,omitempty"`
	Candidates          int                `json:"candidates"`
	Calls               int                `json:"calls"`
	CompileAttempts     int                `json:"compile_attempts"`
	CompilePasses       int                `json:"compile_pass_count"`
	CompileFailures     int                `json:"compile_fail_count"`
	TypecheckFailures   int                `json:"typecheck_fail_count"`
	UsedLocals          int                `json:"used_locals"`
	PublicPassCount     int                `json:"public_pass_count"`
	Strategy            string             `json:"strategy"`
	CompletionPolicy    string             `json:"completion_policy"`
	EffectiveBudgets    map[string]any     `json:"effective_budgets"`
	InputHash           string             `json:"input_hash"`
	HeldoutHash         string             `json:"heldout_hash,omitempty"`
	Runtime             string             `json:"runtime"`
	GOOS                string             `json:"goos"`
	GOARCH              string             `json:"goarch"`
	Ranker              any                `json:"ranker"`
	Usage               *rank.Usage        `json:"usage"`
	EstimatedCost       *float64           `json:"estimated_cost"`
	PhaseMS             map[string]float64 `json:"phase_ms"`
	LatencyMS           float64            `json:"latency_ms"`
}
type measuredRanker struct {
	rank.Ranker
	input, output           int
	inputKnown, outputKnown bool
	calls                   int
	elapsed                 time.Duration
}

type modelFailure struct{ cause error }

func (e modelFailure) Error() string { return "model request failed" }
func (e modelFailure) Unwrap() error { return e.cause }

func checkedScores(candidates []rank.Candidate, scores []rank.Score, err error) ([]rank.Score, error) {
	if err == nil {
		err = rank.Validate(candidates, scores)
	}
	if err != nil {
		return nil, modelFailure{cause: err}
	}
	return scores, nil
}

func (m *measuredRanker) Rank(ctx context.Context, intent string, cs []rank.Candidate) ([]rank.Score, error) {
	start := time.Now()
	defer func() { m.elapsed += time.Since(start) }()
	m.calls++
	if r, ok := m.Ranker.(rank.UsageRanker); ok {
		s, u, e := r.RankWithUsage(ctx, intent, cs)
		if u == nil || u.InputTokens == nil {
			m.inputKnown = false
		} else {
			m.input += *u.InputTokens
		}
		if u == nil || u.OutputTokens == nil {
			m.outputKnown = false
		} else {
			m.output += *u.OutputTokens
		}
		return checkedScores(cs, s, e)
	}
	m.inputKnown = false
	m.outputKnown = false
	scores, err := m.Ranker.Rank(ctx, intent, cs)
	return checkedScores(cs, scores, err)
}
func (m *measuredRanker) usage() *rank.Usage {
	if m.calls == 0 {
		return nil
	}
	u := &rank.Usage{}
	if m.inputKnown {
		u.InputTokens = &m.input
	}
	if m.outputKnown {
		u.OutputTokens = &m.output
	}
	return u
}

// 2026-11-05: 최종 후보를 고정한 후에만 보류 테스트를 읽어 탐색 결과와 독립적으로 평가한다.
func Run(ctx context.Context, p spec.Problem, r rank.Ranker, q Request) (out Result) {
	out.Engine = "ast"
	out.CandidateUnit = "ast_node"
	start := time.Now()
	out.Status = "execution_error"
	out.Strategy = q.Strategy
	out.CompletionPolicy = completionPolicy(q.Strategy)
	out.Runtime = runtime.Version()
	out.GOOS = runtime.GOOS
	out.GOARCH = runtime.GOARCH
	out.PhaseMS = map[string]float64{}
	// 2026-11-05: 잘못된 단가를 청구 추정이나 모델 호출에 사용하지 않는다.
	if p.Validate() != nil || q.MaxCandidates < 0 || q.MaxCalls < 0 || q.MaxDepth < 0 || q.MaxLocals < 0 || q.MaxLocals > 63 || q.MaxStatements < 0 || q.MaxStatements > 63 || q.Timeout < 0 {
		out.Status = "input_error"
		return
	}
	for _, price := range []*float64{q.InputPrice, q.OutputPrice} {
		if price != nil && (*price < 0 || math.IsNaN(*price) || math.IsInf(*price, 0)) {
			out.Status = "input_error"
			return
		}
	}
	if p.Budgets != nil {
		b := p.Budgets
		if q.MaxCandidates <= 0 && b.MaxCandidates != nil {
			q.MaxCandidates = *b.MaxCandidates
		}
		if q.MaxCalls <= 0 && b.MaxCalls != nil {
			q.MaxCalls = *b.MaxCalls
		}
		if q.MaxDepth <= 0 && b.MaxDepth != nil {
			q.MaxDepth = *b.MaxDepth
		}
		if !q.LocalLimitSet && b.MaxLocals != nil {
			q.MaxLocals = *b.MaxLocals
			q.LocalLimitSet = true
		}
		if q.MaxStatements <= 0 && b.MaxStatements != nil {
			q.MaxStatements = *b.MaxStatements
		}
		if q.Timeout <= 0 && b.TimeoutSeconds != nil {
			q.Timeout = time.Duration(*b.TimeoutSeconds) * time.Second
		}
	}
	if q.MaxCandidates <= 0 {
		q.MaxCandidates = 2000
	}
	if q.MaxCalls <= 0 {
		q.MaxCalls = 200
	}
	if q.MaxDepth <= 0 {
		q.MaxDepth = 4
	}
	if !q.LocalLimitSet {
		q.MaxLocals = 2
	}
	if q.MaxStatements <= 0 {
		q.MaxStatements = 8
	}
	if q.Timeout <= 0 {
		q.Timeout = 300 * time.Second
	}
	out.EffectiveBudgets = map[string]any{"max_candidates": q.MaxCandidates, "max_calls": q.MaxCalls, "max_depth": q.MaxDepth, "max_locals": q.MaxLocals, "max_statements": q.MaxStatements, "timeout_seconds": q.Timeout.Seconds()}
	if p.Budgets == nil {
		p.Budgets = &spec.Budgets{}
	} else {
		copyBudgets := *p.Budgets
		p.Budgets = &copyBudgets
	}
	p.Budgets.MaxLocals = &q.MaxLocals
	p.Budgets.MaxStatements = &q.MaxStatements
	p.Budgets.MaxDepth = &q.MaxDepth
	b, _ := json.Marshal(p)
	hash := sha256.Sum256(b)
	out.InputHash = hex.EncodeToString(hash[:])
	ctx, cancel := context.WithTimeout(ctx, q.Timeout)
	defer cancel()
	m := &measuredRanker{Ranker: r, inputKnown: true, outputKnown: true}
	defer func() {
		out.LatencyMS = float64(time.Since(start)) / float64(time.Millisecond)
		out.PhaseMS["rank"] = float64(m.elapsed) / float64(time.Millisecond)
		out.Usage = m.usage()
		if provider, ok := r.(rank.MetadataProvider); ok {
			out.Ranker = provider.Metadata()
		}
		if out.Usage != nil && out.Usage.InputTokens != nil && out.Usage.OutputTokens != nil && q.InputPrice != nil && q.OutputPrice != nil {
			cost := (float64(*out.Usage.InputTokens)**q.InputPrice + float64(*out.Usage.OutputTokens)**q.OutputPrice) / 1e6
			out.EstimatedCost = &cost
		}
		if q.Trace != nil {
			q.Trace(synth.TraceEvent{ID: "run", Reason: "ended:" + out.Status, ElapsedMS: out.LatencyMS})
		}
	}()
	beam := 1
	switch q.Strategy {
	case "greedy", "strict-greedy":
	case "beam":
		beam = 3
	default:
		return
	}
	searchStart := time.Now()
	s, err := synth.Search(ctx, p, m, synth.Options{Beam: beam, MaxCandidates: q.MaxCandidates, MaxCalls: q.MaxCalls, MaxDepth: q.MaxDepth, MaxLocals: q.MaxLocals, LocalLimitSet: true, MaxStatements: q.MaxStatements, Strict: q.Strategy == "strict-greedy", Trace: q.Trace, TraceCode: q.TraceCode}, func(body string) (bool, error) {
		t := time.Now()
		_, e := verify.Source(p, body)
		out.PhaseMS["typecheck"] += float64(time.Since(t)) / float64(time.Millisecond)
		if e != nil {
			out.TypecheckFailures++
			return false, nil
		}
		t = time.Now()
		passed, attempted, compiled, timing, e := verify.CheckMeasured(ctx, p, body, p.PublicTests)
		out.PhaseMS["compile"] += float64(timing.Compile) / float64(time.Millisecond)
		out.PhaseMS["public_execute"] += float64(timing.Execute) / float64(time.Millisecond)
		out.PhaseMS["public_verify"] += float64(time.Since(t)) / float64(time.Millisecond)
		if attempted {
			out.CompileAttempts++
		}
		if compiled {
			out.CompilePasses++
		}
		return passed, e
	})
	searchMS := float64(time.Since(searchStart)) / float64(time.Millisecond)
	out.PhaseMS["generation"] = searchMS - float64(m.elapsed)/float64(time.Millisecond) - out.PhaseMS["typecheck"] - out.PhaseMS["public_verify"]
	if out.PhaseMS["generation"] < 0 {
		out.PhaseMS["generation"] = 0
	}
	out.Candidates = s.Candidates
	out.Calls = s.Calls
	out.UsedLocals = s.UsedLocals
	out.CompileFailures = out.CompileAttempts - out.CompilePasses
	if err != nil {
		var modelErr modelFailure
		switch {
		case errors.Is(err, synth.ErrBudget):
			out.Status = "budget_exhausted"
		case errors.Is(err, synth.ErrNoSolution):
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
	out.PublicPassed = true
	out.PublicPassCount = 1
	out.Source = s.Source
	out.Status = "success"
	if q.Heldout != nil {
		t := time.Now()
		tests, e := q.Heldout()
		if e == nil {
			data, _ := json.Marshal(tests)
			digest := sha256.Sum256(data)
			out.HeldoutHash = hex.EncodeToString(digest[:])
			var passed bool
			passed, e = verify.Check(ctx, p, s.Code, tests)
			if e == nil {
				out.HeldoutPassed = &passed
				if !passed {
					out.Status = "heldout_failed"
				}
			}
		}
		out.PhaseMS["heldout"] = float64(time.Since(t)) / float64(time.Millisecond)
		if e != nil {
			switch {
			case errors.Is(e, context.Canceled):
				out.Status = "cancelled"
			case errors.Is(e, context.DeadlineExceeded):
				out.Status = "timeout"
			default:
				out.Status = "heldout_error"
			}
		}
	}
	return
}
