package runner

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"jevcancode/internal/process"
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
	DecisionBatchSize                                           int
	ReturnPolicy                                                string
	Seed                                                        int64
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
	Engine                     string             `json:"engine"`
	CandidateUnit              string             `json:"candidate_unit"`
	GrammarRevision            string             `json:"grammar_revision,omitempty"`
	TokenCount                 int                `json:"token_count,omitempty"`
	GeneratedTokens            int                `json:"generated_token_count,omitempty"`
	ForcedSteps                int                `json:"forced_step_count,omitempty"`
	ModelSteps                 int                `json:"model_step_count,omitempty"`
	ForcedTokens               int                `json:"forced_tokens,omitempty"`
	ModelSelectedTokens        int                `json:"model_selected_tokens,omitempty"`
	EmittedTokens              []string           `json:"emitted_tokens,omitempty"`
	Status                     string             `json:"status"`
	Source                     string             `json:"source,omitempty"`
	PublicPassed               bool               `json:"public_passed"`
	HeldoutPassed              *bool              `json:"heldout_passed,omitempty"`
	Candidates                 int                `json:"candidates"`
	Calls                      int                `json:"calls"`
	Questions                  int                `json:"questions"`
	HTTPRequests               *int64             `json:"http_requests"`
	Expansions                 int                `json:"expansions"`
	MemoHits                   int                `json:"memo_hits"`
	EvaluationMemoHits         int                `json:"evaluation_memo_hits"`
	Validations                int                `json:"validations"`
	ErrorCode                  string             `json:"error_code,omitempty"`
	CompileAttempts            int                `json:"compile_attempts"`
	CompilePasses              int                `json:"compile_pass_count"`
	CompileFailures            int                `json:"compile_fail_count"`
	TypecheckFailures          int                `json:"typecheck_fail_count"`
	UsedLocals                 int                `json:"used_locals"`
	PublicPassCount            int                `json:"public_pass_count"`
	Strategy                   string             `json:"strategy"`
	ReturnPolicy               string             `json:"return_policy,omitempty"`
	ValidationMode             string             `json:"validation_mode,omitempty"`
	Heuristic                  string             `json:"heuristic,omitempty"`
	DecisionBatchSize          int                `json:"decision_batch_size"`
	DecisionMode               string             `json:"decision_mode"`
	Seed                       int64              `json:"seed"`
	CompletionPolicy           string             `json:"completion_policy"`
	EffectiveBudgets           map[string]any     `json:"effective_budgets"`
	InputHash                  string             `json:"input_hash"`
	HeldoutHash                string             `json:"heldout_hash,omitempty"`
	Runtime                    string             `json:"runtime"`
	GOOS                       string             `json:"goos"`
	GOARCH                     string             `json:"goarch"`
	Ranker                     any                `json:"ranker"`
	Usage                      *rank.Usage        `json:"usage"`
	KnownUsage                 *rank.Usage        `json:"known_usage"`
	EstimatedCost              *float64           `json:"estimated_cost"`
	KnownEstimatedCost         *float64           `json:"known_estimated_cost"`
	PhaseMS                    map[string]float64 `json:"phase_ms"`
	LatencyMS                  float64            `json:"latency_ms"`
	ResourceScope              string             `json:"resource_scope"`
	ProcessPeakWorkingSetBytes *uint64            `json:"process_peak_working_set_bytes"`
	ClientProcessCPUMS         *float64           `json:"client_process_cpu_ms"`
	ServerCPUMS                *float64           `json:"server_cpu_ms"`
	ServerComputeMS            *float64           `json:"server_compute_ms"`
}

// 2026-11-12: 서버 자원은 관측하지 않고 클라이언트의 누적 수명 기준만 명시한다.
func (out *Result) sampleResources() {
	snapshot := process.CurrentResources()
	out.ResourceScope = snapshot.Scope
	out.ProcessPeakWorkingSetBytes = snapshot.PeakWorkingSetBytes
	out.ClientProcessCPUMS = snapshot.CPUTimeMS
}

type measuredRanker struct {
	rank.Ranker
	input, output                 int
	inputKnown, outputKnown       bool
	inputObserved, outputObserved bool
	calls                         int
	elapsed                       time.Duration
}

func budgetRejected(err error) bool {
	var coded interface{ ErrorCode() string }
	return errors.As(err, &coded) && coded.ErrorCode() == "question_budget_exhausted"
}

// 2026-11-12: 계측으로 감싼 순위기의 확률 계약을 보존하고 임의 점수는 승격하지 않는다.
func (m *measuredRanker) NormalizedCategorical() bool {
	r, ok := m.Ranker.(rank.CategoricalRanker)
	return ok && r.NormalizedCategorical()
}

// 2026-11-12: 래퍼는 실제 전송 batch 지원 여부를 전달하고 usage는 묶음당 한 번만 집계한다.
func (m *measuredRanker) SupportsDecisionBatch() bool {
	_, ok := m.Ranker.(rank.BatchDecisionRanker)
	if capability, exists := m.Ranker.(rank.BatchCapability); exists {
		return ok && capability.SupportsDecisionBatch()
	}
	return ok
}
func (m *measuredRanker) RankDecisions(ctx context.Context, qs []rank.DecisionRequest) ([]rank.Decision, *rank.Usage, error) {
	if batch, ok := m.Ranker.(rank.BatchDecisionRanker); ok && m.SupportsDecisionBatch() {
		start := time.Now()
		defer func() { m.elapsed += time.Since(start) }()
		m.calls += len(qs)
		ds, u, err := batch.RankDecisions(ctx, qs)
		if budgetRejected(err) {
			m.calls -= len(qs)
			return nil, nil, modelFailure{cause: err}
		}
		m.recordUsage(u)
		if err != nil {
			return nil, u, modelFailure{cause: err}
		}
		if len(ds) != len(qs) {
			return nil, u, modelFailure{cause: errors.New("batch decision count mismatch")}
		}
		for i := range ds {
			ds[i].Usage = nil
			ds[i].Scores, err = checkedScores(qs[i].Candidates, ds[i].Scores, nil)
			if err != nil {
				return nil, u, err
			}
		}
		return ds, u, nil
	}
	results := make([]rank.Decision, 0, len(qs))
	input, output := 0, 0
	inputKnown, outputKnown := true, true
	for _, q := range qs {
		d, err := m.RankDecision(ctx, q.State, q.Candidates)
		if err != nil {
			return nil, nil, err
		}
		if d.Usage == nil || d.Usage.InputTokens == nil {
			inputKnown = false
		} else {
			input += *d.Usage.InputTokens
		}
		if d.Usage == nil || d.Usage.OutputTokens == nil {
			outputKnown = false
		} else {
			output += *d.Usage.OutputTokens
		}
		d.Usage = nil
		results = append(results, d)
	}
	u := &rank.Usage{}
	if inputKnown {
		u.InputTokens = &input
	}
	if outputKnown {
		u.OutputTokens = &output
	}
	return results, u, nil
}
func (m *measuredRanker) Metadata() rank.Metadata {
	if provider, ok := m.Ranker.(rank.MetadataProvider); ok {
		metadata := provider.Metadata()
		metadata.ReturnedModel = ""
		return metadata
	}
	return rank.Metadata{}
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
	d, err := m.RankDecision(ctx, intent, cs)
	return d.Scores, err
}

// 2026-11-08: 측정 래퍼가 응답 선택을 잃지 않으면서 기존 순위기와 사용량을 보존한다.
func (m *measuredRanker) RankDecision(ctx context.Context, intent string, cs []rank.Candidate) (rank.Decision, error) {
	start := time.Now()
	defer func() { m.elapsed += time.Since(start) }()
	m.calls++
	if r, ok := m.Ranker.(rank.DecisionRanker); ok {
		d, e := r.RankDecision(ctx, intent, cs)
		if budgetRejected(e) {
			m.calls--
			return rank.Decision{}, modelFailure{cause: e}
		}
		m.recordUsage(d.Usage)
		d.Scores, e = checkedScores(cs, d.Scores, e)
		if e != nil {
			return rank.Decision{}, e
		}
		return d, nil
	}
	if r, ok := m.Ranker.(rank.UsageRanker); ok {
		s, u, e := r.RankWithUsage(ctx, intent, cs)
		if budgetRejected(e) {
			m.calls--
			return rank.Decision{}, modelFailure{cause: e}
		}
		m.recordUsage(u)
		s, e = checkedScores(cs, s, e)
		return rank.Decision{Scores: s, Usage: u}, e
	}
	scores, err := m.Ranker.Rank(ctx, intent, cs)
	if budgetRejected(err) {
		m.calls--
		return rank.Decision{}, modelFailure{cause: err}
	}
	m.inputKnown = false
	m.outputKnown = false
	scores, err = checkedScores(cs, scores, err)
	return rank.Decision{Scores: scores}, err
}
func (m *measuredRanker) recordUsage(u *rank.Usage) {
	if u == nil || u.InputTokens == nil {
		m.inputKnown = false
	} else {
		m.input += *u.InputTokens
		m.inputObserved = true
	}
	if u == nil || u.OutputTokens == nil {
		m.outputKnown = false
	} else {
		m.output += *u.OutputTokens
		m.outputObserved = true
	}
}

// 2026-11-12: 일부 응답이 usage를 생략해도 관측된 소계는 전체 미확인 값과 분리한다.
func (m *measuredRanker) knownUsage() *rank.Usage {
	if !m.inputObserved && !m.outputObserved {
		return nil
	}
	u := &rank.Usage{}
	if m.inputObserved {
		u.InputTokens = &m.input
	}
	if m.outputObserved {
		u.OutputTokens = &m.output
	}
	return u
}
func (out *Result) recordUsage(m *measuredRanker, inputPrice, outputPrice *float64) {
	out.Usage = m.usage()
	out.KnownUsage = m.knownUsage()
	if out.Usage != nil && out.Usage.InputTokens != nil && out.Usage.OutputTokens != nil && inputPrice != nil && outputPrice != nil {
		cost := (float64(*out.Usage.InputTokens)**inputPrice + float64(*out.Usage.OutputTokens)**outputPrice) / 1e6
		out.EstimatedCost = &cost
	}
	if out.KnownUsage != nil {
		cost := 0.0
		known := false
		if out.KnownUsage.InputTokens != nil && inputPrice != nil {
			cost += float64(*out.KnownUsage.InputTokens) * *inputPrice / 1e6
			known = true
		}
		if out.KnownUsage.OutputTokens != nil && outputPrice != nil {
			cost += float64(*out.KnownUsage.OutputTokens) * *outputPrice / 1e6
			known = true
		}
		if known {
			out.KnownEstimatedCost = &cost
		}
	}
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
	defer out.sampleResources()
	// 2026-11-12: 호출자가 지정한 반환 정책을 AST 생성과 최종 검증에 동일하게 적용한다.
	if q.ReturnPolicy != "" {
		p.ReturnPolicy = q.ReturnPolicy
	}
	out.Engine = "ast"
	out.CandidateUnit = "ast_node"
	start := time.Now()
	out.Status = "execution_error"
	out.Strategy = q.Strategy
	out.ReturnPolicy = p.ReturnPolicy
	out.CompletionPolicy = completionPolicy(q.Strategy)
	out.Runtime = runtime.Version()
	out.GOOS = runtime.GOOS
	out.GOARCH = runtime.GOARCH
	out.PhaseMS = map[string]float64{}
	// 2026-11-05: 잘못된 단가를 청구 추정이나 모델 호출에 사용하지 않는다.
	if p.Validate() != nil || q.DecisionBatchSize < 0 || q.DecisionBatchSize > 1 || q.MaxCandidates < 0 || q.MaxCalls < 0 || q.MaxDepth < 0 || q.MaxLocals < 0 || q.MaxLocals > 63 || q.MaxStatements < 0 || q.MaxStatements > 63 || q.Timeout < 0 {
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
		out.recordUsage(m, q.InputPrice, q.OutputPrice)
		if provider, ok := r.(rank.MetadataProvider); ok {
			out.Ranker = provider.Metadata()
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
	out.Questions = m.calls
	out.UsedLocals = s.UsedLocals
	out.CompileFailures = out.CompileAttempts - out.CompilePasses
	if err != nil {
		var modelErr modelFailure
		var coded interface{ ErrorCode() string }
		if errors.As(err, &coded) {
			out.ErrorCode = coded.ErrorCode()
		}
		switch {
		case out.ErrorCode == "question_budget_exhausted":
			out.Status = "budget_exhausted"
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
