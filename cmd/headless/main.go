package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"reflect"
	"sync"
	"time"

	"jevcancode/internal/application"
	"jevcancode/internal/compare"
	"jevcancode/internal/modelprovider"
	"jevcancode/internal/rank"
	"jevcancode/internal/runner"
)

type resolver func(context.Context, compare.Model) (rank.Ranker, error)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	os.Exit(execute(ctx, os.Args[1:], os.Stdout, os.Stderr, nil))
}

// 2026-11-12: 출력과 모델 공급자를 분리하여 실제 비용 없이 같은 Manager 경로를 검증한다.
func execute(ctx context.Context, args []string, out, stderr io.Writer, resolve resolver) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, "명령이 필요합니다: run, inspect, compare")
		return 2
	}
	switch args[0] {
	case "--help", "-h", "help":
		fmt.Fprintln(out, "사용법: headless run|inspect|compare|audit [옵션]\nrun: --decision-batch-size N; audit: --trace PATH --seq N --max-questions N")
		return 0
	case "audit":
		return audit(ctx, args[1:], out, stderr, resolve)
	case "run":
		return run(ctx, args[1:], out, stderr, resolve)
	case "inspect":
		return inspect(args[1:], out, stderr)
	case "compare":
		return compareRecords(args[1:], out, stderr)
	default:
		fmt.Fprintln(stderr, "알 수 없는 명령")
		return 2
	}
}
func flags(name string, stderr io.Writer) *flag.FlagSet {
	f := flag.NewFlagSet(name, flag.ContinueOnError)
	f.SetOutput(stderr)
	return f
}
func encode(out io.Writer, value any) error { return json.NewEncoder(out).Encode(value) }
func fail(stderr io.Writer, msg string) int { fmt.Fprintln(stderr, msg); return 2 }

func run(ctx context.Context, args []string, out, stderr io.Writer, resolve resolver) (code int) {
	f := flags("run", stderr)
	kind := f.String("kind", "search", "search or compare")
	spec := f.String("spec", "", "problem specification")
	manifest := f.String("manifest", "", "comparison manifest")
	problem := f.String("problem", "all", "problem ID")
	engine := f.String("engine", "token", "token or ast")
	strategy := f.String("strategy", "beam", "search strategy")
	model := f.String("model", "jev", "jev or local")
	output := f.String("output", "output", "artifact directory")
	python := f.String("python", "", "local Python executable path")
	worker := f.String("worker", "", "local worker script path")
	modelDir := f.String("model-dir", "", "local model directory")
	maxQuestions := f.Int("max-questions", 0, "maximum model questions")
	maxCalls := f.Int("max-calls", 0, "maximum model calls per search")
	batchSize := f.Int("decision-batch-size", 1, "independent questions per batch")
	maxCandidates := f.Int("max-candidates", 0, "maximum candidate expansions")
	maxTokens := f.Int("max-tokens", 0, "maximum tokens")
	maxDepth := f.Int("max-depth", 0, "maximum depth")
	maxNesting := f.Int("max-nesting", 0, "maximum nesting")
	maxLocals := f.Int("max-locals", 0, "maximum locals")
	maxStatements := f.Int("max-statements", 0, "maximum statements")
	timeout := f.Duration("timeout", 0, "search timeout")
	seed := f.Int64("seed", 42, "random seed")
	policy := f.String("return-policy", "values-only-v1", "return policy")
	jsonOut := f.Bool("json", false, "JSON summary")
	progress := f.Bool("progress", false, "JSONL progress on stdout")
	if f.Parse(args) != nil || f.NArg() != 0 {
		return 2
	}
	if (*kind != "search" && *kind != "compare") || (*spec == "") == (*manifest == "") || (*kind == "compare" && *manifest == "") || (*engine != "token" && *engine != "ast") || *maxQuestions <= 0 || *maxCalls < 0 || *maxCandidates < 0 || *maxTokens < 0 || *maxDepth < 0 || *maxNesting < 0 || *maxLocals < 0 || *maxStatements < 0 || *timeout < 0 || *policy != "values-only-v1" {
		return fail(stderr, "잘못된 입력 또는 필수 질문 예산 누락")
	}
	if *strategy != "beam" && *strategy != "greedy" && *strategy != "strict-greedy" && *strategy != "lds" && *strategy != "levin" && *strategy != "phs" && *strategy != "mcts" {
		return fail(stderr, "잘못된 전략")
	}
	if *batchSize < 1 || *batchSize > 256 || (*kind == "search" && *batchSize > 1 && (*strategy != "lds" && *strategy != "levin" && *strategy != "phs")) {
		return fail(stderr, "지원하지 않는 batch 크기 또는 전략")
	}
	if *model != "jev" && *model != "local" {
		return fail(stderr, "지원하지 않는 모델")
	}
	if *model == "local" && (*python == "" || *worker == "" || *modelDir == "") {
		return fail(stderr, "local_runtime_not_configured")
	}
	if resolve == nil {
		cfg := modelprovider.Config{}
		var err error
		if *python != "" {
			cfg.Python, err = filepath.Abs(*python)
			if err != nil {
				return fail(stderr, "invalid_input")
			}
		}
		if *worker != "" {
			cfg.Script, err = filepath.Abs(*worker)
			if err != nil {
				return fail(stderr, "invalid_input")
			}
		}
		if *modelDir != "" {
			cfg.ModelDir, err = filepath.Abs(*modelDir)
			if err != nil {
				return fail(stderr, "invalid_input")
			}
		}
		p := modelprovider.New(cfg)
		resolve = p.Resolve
		defer func() {
			done := make(chan error, 1)
			go func() { done <- p.Close() }()
			select {
			case err := <-done:
				if err != nil {
					fmt.Fprintln(stderr, "모델 종료 실패")
					code = 1
				}
			case <-time.After(15 * time.Second):
				fmt.Fprintln(stderr, "모델 종료 시간 초과")
				code = 1
			}
		}()
	}
	// 2026-11-12: 모든 trial이 하나의 예산을 공유해 비교 실행에서 유료 호출 상한을 우회하지 못하게 한다.
	gate := &questionGate{limit: *maxQuestions}
	wrapped := func(ctx context.Context, m compare.Model) (rank.Ranker, error) {
		r, e := resolve(ctx, m)
		if e != nil {
			return nil, e
		}
		return gate.wrap(r), nil
	}
	m := application.New(application.Config{OutputDir: *output, Resolve: wrapped})
	// 2026-11-12: 명시한 0과 생략을 구분해 문제의 기본 locals 한도가 선택을 덮어쓰지 않도록 한다.
	localLimitSet := false
	f.Visit(func(value *flag.Flag) {
		if value.Name == "max-locals" {
			localLimitSet = true
		}
	})
	q := application.Request{Kind: *kind, Engine: *engine, Strategy: *strategy, ManifestPath: *manifest, SpecPath: *spec, ProblemID: *problem, Model: compare.Model{ID: *model, Ranker: *model, Model: "jev-1.14.0", KeyEnv: "TYPESAFE_API_KEY"}, Options: runner.Request{DecisionBatchSize: *batchSize, MaxCalls: *maxCalls, MaxCandidates: *maxCandidates, MaxTokens: *maxTokens, MaxDepth: *maxDepth, MaxNesting: *maxNesting, MaxLocals: *maxLocals, LocalLimitSet: localLimitSet, MaxStatements: *maxStatements, Timeout: *timeout, Seed: *seed, ReturnPolicy: *policy}}
	if *model == "local" {
		q.Model.Model = "Mapika/decider-4b"
	}
	if *spec != "" && *problem == "all" {
		q.ProblemID = filepath.Base(*spec)
	}
	id, err := m.Start(q)
	if err != nil {
		return fail(stderr, "실행을 시작할 수 없습니다")
	}
	cursor := application.TraceCursor{}
	drain := func() error {
		for {
			batch, next, more, e := m.ReadTraceBatch(id, cursor, 256)
			if e != nil {
				return e
			}
			cursor = next
			if *progress {
				for _, ev := range batch {
					if e = encode(out, ev); e != nil {
						return e
					}
				}
			}
			if !more {
				return nil
			}
		}
	}
	ctxDone := ctx.Done()
	var cleanup <-chan time.Time
	for {
		if err = drain(); err != nil {
			_ = m.Cancel()
			break
		}
		s := m.Snapshot()
		if s.Status != "running" && s.Status != "cancelling" {
			break
		}
		select {
		case <-m.Changed():
		case <-ctxDone:
			_ = m.Cancel()
			ctxDone = nil
			cleanup = time.After(15 * time.Second)
		case <-cleanup:
			fmt.Fprintln(stderr, "취소 정리 시간 초과")
			return 1
		}
	}
	closeCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if e := m.Close(closeCtx); e != nil {
		fmt.Fprintln(stderr, "종료 정리 시간 초과")
		return 1
	}
	if err != nil {
		fmt.Fprintln(stderr, "trace 읽기 실패")
		return 1
	}
	if err = drain(); err != nil {
		fmt.Fprintln(stderr, "최종 trace 읽기 실패")
		return 1
	}
	s := m.Snapshot()
	summary := map[string]any{"status": s.Status, "run_id": id, "result_path": filepath.Join(*output, id), "trace_path": filepath.Join(*output, id+".trace.jsonl"), "results": s.Results}
	if len(s.Payload) > 0 {
		summary["report"] = s.Payload
	}
	if *jsonOut {
		if *progress {
			summary["kind"] = "summary"
		}
		if encode(out, summary) != nil {
			return 1
		}
	}
	fmt.Fprintf(stderr, "실행 상태: %s (%s)\n", s.Status, id)
	if s.JobKind == "compare" {
		var report compare.Report
		if json.Unmarshal(s.Payload, &report) != nil || report.Total == 0 || report.Successful != report.Total {
			return 1
		}
	}
	if s.Status != "completed" {
		return 1
	}
	for _, r := range s.Results {
		if r.Result.Status != "success" {
			return 1
		}
	}
	return 0
}

func inspect(args []string, out, stderr io.Writer) int {
	f := flags("inspect", stderr)
	dir := f.String("output", "output", "artifact directory")
	id := f.String("run", "", "run ID")
	_ = f.Bool("json", false, "JSON output")
	if f.Parse(args) != nil || f.NArg() != 0 {
		return 2
	}
	m := application.New(application.Config{OutputDir: *dir})
	if *id == "" {
		records, e := m.History()
		if e != nil {
			return fail(stderr, "기록을 읽을 수 없습니다")
		}
		if encode(out, records) != nil {
			return 1
		}
		return 0
	}
	record, e := m.ReadHistory(*id)
	if e != nil {
		return fail(stderr, "기록을 읽을 수 없습니다")
	}
	if encode(out, record) != nil {
		return 1
	}
	return 0
}

type stringsFlag []string

func (s *stringsFlag) String() string     { return fmt.Sprint([]string(*s)) }
func (s *stringsFlag) Set(v string) error { *s = append(*s, v); return nil }
func compareRecords(args []string, out, stderr io.Writer) int {
	f := flags("compare", stderr)
	dir := f.String("output", "output", "artifact directory")
	var ids stringsFlag
	f.Var(&ids, "run", "run ID (repeat)")
	_ = f.Bool("json", false, "JSON output")
	if f.Parse(args) != nil || f.NArg() != 0 || len(ids) < 2 {
		return fail(stderr, "비교할 기록을 두 개 이상 지정하세요")
	}
	m := application.New(application.Config{OutputDir: *dir})
	var records []application.Record
	var first runner.Result
	for i, id := range ids {
		rec, e := m.ReadHistory(id)
		if e != nil {
			return fail(stderr, "기록을 읽을 수 없습니다")
		}
		var snap application.Snapshot
		if json.Unmarshal(rec.Data, &snap) != nil || len(snap.Results) != 1 {
			return fail(stderr, "비교할 수 없는 기록")
		}
		var raw struct {
			Results []struct {
				Result struct {
					Seed         *int64  `json:"seed"`
					ReturnPolicy *string `json:"return_policy"`
				} `json:"result"`
			} `json:"results"`
		}
		if json.Unmarshal(rec.Data, &raw) != nil || len(raw.Results) != 1 || raw.Results[0].Result.Seed == nil || raw.Results[0].Result.ReturnPolicy == nil || *raw.Results[0].Result.ReturnPolicy == "" {
			return fail(stderr, "비교 조건 메타데이터 없음")
		}
		r := snap.Results[0].Result
		if i == 0 {
			first = r
		} else if first.Engine != r.Engine || first.InputHash != r.InputHash || first.HeldoutHash != r.HeldoutHash || first.Seed != r.Seed || first.ReturnPolicy != r.ReturnPolicy || !reflect.DeepEqual(first.EffectiveBudgets, r.EffectiveBudgets) {
			return fail(stderr, "비교 조건이 다릅니다")
		}
		records = append(records, rec)
	}
	if encode(out, records) != nil {
		return 1
	}
	return 0
}

type questionGate struct {
	mu          sync.Mutex
	used, limit int
}
type gated struct {
	gate  *questionGate
	inner rank.Ranker
}

func (g *questionGate) wrap(r rank.Ranker) rank.Ranker {
	base := &gated{g, r}
	if counter, ok := r.(rank.HTTPRequestCounter); ok {
		return &gatedHTTP{gated: base, counter: counter}
	}
	return base
}

type gatedHTTP struct {
	*gated
	counter rank.HTTPRequestCounter
}

func (g *gatedHTTP) HTTPRequests() int64 { return g.counter.HTTPRequests() }
func (g *gated) take(n int) error {
	g.gate.mu.Lock()
	defer g.gate.mu.Unlock()
	if n < 1 || n > g.gate.limit-g.gate.used {
		return &rank.SafeError{Code: "question_budget_exhausted", Message: "question budget exhausted"}
	}
	g.gate.used += n
	return nil
}
func (g *gated) Rank(ctx context.Context, intent string, cs []rank.Candidate) ([]rank.Score, error) {
	d, e := g.RankDecision(ctx, intent, cs)
	return d.Scores, e
}
func (g *gated) RankDecision(ctx context.Context, intent string, cs []rank.Candidate) (rank.Decision, error) {
	if e := g.take(1); e != nil {
		return rank.Decision{}, e
	}
	if r, ok := g.inner.(rank.DecisionRanker); ok {
		return r.RankDecision(ctx, intent, cs)
	}
	if r, ok := g.inner.(rank.UsageRanker); ok {
		s, u, e := r.RankWithUsage(ctx, intent, cs)
		return rank.Decision{Scores: s, Usage: u}, e
	}
	s, e := g.inner.Rank(ctx, intent, cs)
	return rank.Decision{Scores: s}, e
}
func (g *gated) Metadata() rank.Metadata {
	if r, ok := g.inner.(rank.MetadataProvider); ok {
		return r.Metadata()
	}
	return rank.Metadata{}
}

func (g *gated) NormalizedCategorical() bool {
	r, ok := g.inner.(rank.CategoricalRanker)
	return ok && r.NormalizedCategorical()
}

// 2026-11-12: 독립 질문 단위로 원자적으로 차감하여 batch 전송이 전역 한도를 우회하지 못하게 한다.
func (g *gated) SupportsDecisionBatch() bool {
	_, ok := g.inner.(rank.BatchDecisionRanker)
	if c, exists := g.inner.(rank.BatchCapability); exists {
		return ok && c.SupportsDecisionBatch()
	}
	return ok
}
func (g *gated) RankDecisions(ctx context.Context, qs []rank.DecisionRequest) ([]rank.Decision, *rank.Usage, error) {
	if !g.SupportsDecisionBatch() {
		return nil, nil, &rank.SafeError{Code: "unsupported_batch", Message: "unsupported batch"}
	}
	if err := g.take(len(qs)); err != nil {
		return nil, nil, err
	}
	return g.inner.(rank.BatchDecisionRanker).RankDecisions(ctx, qs)
}
