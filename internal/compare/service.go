package compare

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"tessaro/internal/rank"
	"tessaro/internal/runner"
	"tessaro/internal/spec"
	"tessaro/internal/synth"
	"tessaro/internal/tokenspec"
	"tessaro/internal/tokensynth"
	"os"
	"path/filepath"
	"time"
)

type Outcome struct {
	Error      string        `json:"error,omitempty"`
	TrialIndex int           `json:"trial_index"`
	Trial      Trial         `json:"trial"`
	Result     runner.Result `json:"result"`
	ElapsedMS  float64       `json:"elapsed_ms"`
}
type Group struct {
	Engine                string         `json:"engine"`
	Model                 string         `json:"model"`
	Strategy              string         `json:"strategy"`
	ValidationMode        string         `json:"validation_mode,omitempty"`
	Total                 int            `json:"total"`
	PlannedSuccessRate    *float64       `json:"planned_success_rate"`
	Attempted             int            `json:"attempted"`
	Successful            int            `json:"successful"`
	Failed                int            `json:"failed"`
	NotRunCancelled       int            `json:"not_run_cancelled"`
	NotRunBudget          int            `json:"not_run_budget"`
	SuccessRate           *float64       `json:"success_rate"`
	AllAttemptMeanMS      *float64       `json:"all_attempt_mean_ms"`
	SuccessfulMeanMS      *float64       `json:"successful_mean_ms"`
	StatusCounts          map[string]int `json:"status_counts"`
	Calls                 int            `json:"calls"`
	KnownInputTokens      int            `json:"known_input_tokens"`
	KnownOutputTokens     int            `json:"known_output_tokens"`
	UnknownUsageCount     int            `json:"unknown_usage_count"`
	KnownCostSubtotal     float64        `json:"known_cost_subtotal"`
	KnownCostObservations int            `json:"known_cost_observations"`
	UnknownCostCount      int            `json:"unknown_cost_count"`
}
type Report struct {
	Engine             string            `json:"engine"`
	Seed               int64             `json:"seed"`
	Shuffle            string            `json:"shuffle"`
	ConfigHash         string            `json:"config_hash"`
	ConfigHashVersion  string            `json:"config_hash_version"`
	ConfigHashError    string            `json:"config_hash_error,omitempty"`
	RunOptions         RunOptions        `json:"run_options"`
	InputHashes        map[string]string `json:"input_hashes"`
	HeldoutHashes      map[string]string `json:"heldout_hashes"`
	Total              int               `json:"total"`
	Attempted          int               `json:"attempted"`
	NotRunCancelled    int               `json:"not_run_cancelled"`
	NotRunBudget       int               `json:"not_run_budget"`
	Successful         int               `json:"successful"`
	Failed             int               `json:"failed"`
	SuccessRate        *float64          `json:"success_rate"`
	PlannedSuccessRate *float64          `json:"planned_success_rate"`
	AllAttemptMeanMS   *float64          `json:"all_attempt_mean_ms"`
	SuccessfulMeanMS   *float64          `json:"successful_mean_ms"`
	Groups             []Group           `json:"groups"`
	EvaluationGroups   []Group           `json:"evaluation_groups"`
	Outcomes           []Outcome         `json:"outcomes"`
}

// 2026-11-12: callback이나 인증값 없이 실험의 요청 예산과 정책을 해시에 포함한다.
type RunOptions struct {
	ReturnPolicy      string  `json:"return_policy"`
	DecisionBatchSize int     `json:"decision_batch_size"`
	MaxTokens         int     `json:"max_tokens"`
	MaxNesting        int     `json:"max_nesting"`
	MaxCandidates     int     `json:"max_candidates"`
	MaxCalls          int     `json:"max_calls"`
	MaxDepth          int     `json:"max_depth"`
	MaxLocals         int     `json:"max_locals"`
	LocalLimitSet     bool    `json:"local_limit_set"`
	MaxStatements     int     `json:"max_statements"`
	TimeoutSeconds    float64 `json:"timeout_seconds"`
	TrialSeedBase     int64   `json:"trial_seed_base"`
}
type cachedProblem struct {
	p          spec.Problem
	token      tokenspec.Problem
	err        error
	inputError string
}

// 2026-11-12: 비교 manifest의 절대 경로는 그대로 쓰고 상대 경로만 manifest 기준으로 해석한다.
func inputPath(base, path string) string {
	if filepath.IsAbs(path) {
		return path
	}
	return filepath.Join(base, path)
}

type cachedEvaluation struct {
	tests      []spec.Test
	tokenTests []tokenspec.Test
	err        error
}

// 2026-11-05: 실패를 분모에서 숨기지 않고 동일 계획을 순차 실행한다.
func Run(ctx context.Context, base string, m Manifest, trace func(any), traceCode ...bool) Report {
	return RunWithResolver(ctx, base, m, trace, func(_ context.Context, model Model) (rank.Ranker, error) { return model.NewRanker(), nil }, traceCode...)
}

// 2026-11-07: 모든 비교 trial이 데스크톱에서 관리하는 동일한 모델 공급자를 사용한다.
func RunWithResolver(ctx context.Context, base string, m Manifest, trace func(any), resolve func(context.Context, Model) (rank.Ranker, error), traceCode ...bool) Report {
	return RunWithOptions(ctx, base, m, trace, resolve, runner.Request{}, traceCode...)
}

// 2026-11-12: 비교 trial에도 호출자가 정한 실행 예산과 반환 정책을 전달한다.
func RunWithOptions(ctx context.Context, base string, m Manifest, trace func(any), resolve func(context.Context, Model) (rank.Ranker, error), options runner.Request, traceCode ...bool) Report {
	if m.Engine == "" {
		m.Engine = "token"
	}
	plan := Plan(m)
	report := Report{Engine: m.Engine, Seed: m.Seed, Shuffle: "Go math/rand seeded shuffle of problems", Total: len(plan), InputHashes: map[string]string{}, HeldoutHashes: map[string]string{}, ConfigHashVersion: "v2", RunOptions: RunOptions{ReturnPolicy: options.ReturnPolicy, DecisionBatchSize: options.DecisionBatchSize, MaxTokens: options.MaxTokens, MaxNesting: options.MaxNesting, MaxCandidates: options.MaxCandidates, MaxCalls: options.MaxCalls, MaxDepth: options.MaxDepth, MaxLocals: options.MaxLocals, LocalLimitSet: options.LocalLimitSet, MaxStatements: options.MaxStatements, TimeoutSeconds: options.Timeout.Seconds(), TrialSeedBase: 42}}
	models := map[string]Model{}
	for _, x := range m.Models {
		models[x.ID] = x
	}
	problems := map[string]Problem{}
	cached := map[string]cachedProblem{}
	evaluations := map[string]cachedEvaluation{}
	for _, x := range m.Problems {
		problems[x.ID] = x
		report.HeldoutHashes[x.ID] = "not_loaded"
		if x.Heldout == "" {
			report.HeldoutHashes[x.ID] = "not_configured"
		}
		if ctx.Err() != nil {
			report.InputHashes[x.ID] = "not_loaded"
			continue
		}
		f, err := os.Open(inputPath(base, x.Spec))
		inputError := ""
		if err != nil {
			inputError = "input_unavailable"
		}
		var p spec.Problem
		var tokenProblem tokenspec.Problem
		if err == nil {
			if m.Engine == "token" {
				tokenProblem, err = tokenspec.Decode(f)
			} else {
				p, err = spec.Decode(f)
			}
			f.Close()
			if err != nil {
				inputError = "input_invalid"
			}
		}
		cached[x.ID] = cachedProblem{p: p, token: tokenProblem, err: err, inputError: inputError}
		if err != nil {
			report.InputHashes[x.ID] = "invalid"
		} else {
			var value any = p
			if m.Engine == "token" {
				value = tokenProblem
			}
			data, _ := json.Marshal(value)
			hash := sha256.Sum256(data)
			report.InputHashes[x.ID] = hex.EncodeToString(hash[:])
		}
	}
	var all, success float64
	stageBudgetExhausted := false
	for idx, trial := range plan {
		out := Outcome{TrialIndex: idx + 1, Trial: trial}
		out.Result.Engine = m.Engine
		if value, loaded := cached[trial.Problem]; loaded && value.err == nil && m.Engine == "token" {
			out.Result.ValidationMode = value.token.ValidationMode
		}
		if stageBudgetExhausted {
			out.Result.Status = "not_run_budget"
			report.NotRunBudget++
			report.Outcomes = append(report.Outcomes, out)
			continue
		}
		if ctx.Err() != nil {
			out.Result.Status = "not_run_cancelled"
			report.NotRunCancelled++
			report.Outcomes = append(report.Outcomes, out)
			continue
		}
		start := time.Now()
		report.Attempted++
		problem := problems[trial.Problem]
		model := models[trial.Model]
		data := cached[trial.Problem]
		p, err := data.p, data.err
		if err != nil {
			out.Result.Status = "input_error"
			out.Result.ErrorCode = data.inputError
			out.Error = data.inputError
		} else {
			q := options
			q.Strategy, q.Seed, q.InputPrice, q.OutputPrice = trial.Strategy, trial.Seed, model.InputPrice, model.OutputPrice
			if problem.Heldout != "" && m.Engine == "ast" {
				// 2026-11-05: 첫 제출 후 읽은 평가셋을 고정해 조합 사이 파일 변경이 실험 조건을 바꾸지 못하게 한다.
				q.Heldout = func() ([]spec.Test, error) {
					evaluation, loaded := evaluations[trial.Problem]
					if !loaded {
						f, e := os.Open(inputPath(base, problem.Heldout))
						if e == nil {
							evaluation.tests, e = spec.DecodeTests(f, p)
							f.Close()
						}
						evaluation.err = e
						evaluations[trial.Problem] = evaluation
						if e == nil {
							data, _ := json.Marshal(evaluation.tests)
							digest := sha256.Sum256(data)
							report.HeldoutHashes[trial.Problem] = hex.EncodeToString(digest[:])
						} else {
							report.HeldoutHashes[trial.Problem] = "unavailable"
						}
					}
					return evaluation.tests, evaluation.err
				}
			}
			if problem.Heldout != "" && m.Engine == "token" {
				q.TokenHeldout = func() ([]tokenspec.Test, error) {
					evaluation, loaded := evaluations[trial.Problem]
					if !loaded {
						f, e := os.Open(inputPath(base, problem.Heldout))
						if e == nil {
							evaluation.tokenTests, e = tokenspec.DecodeTests(f, data.token)
							f.Close()
						}
						evaluation.err = e
						evaluations[trial.Problem] = evaluation
						if e == nil {
							encoded, _ := json.Marshal(evaluation.tokenTests)
							digest := sha256.Sum256(encoded)
							report.HeldoutHashes[trial.Problem] = hex.EncodeToString(digest[:])
						} else {
							report.HeldoutHashes[trial.Problem] = "unavailable"
						}
					}
					return evaluation.tokenTests, evaluation.err
				}
			}
			if trace != nil {
				q.TraceCode = len(traceCode) > 0 && traceCode[0]
				q.Trace = func(event synth.TraceEvent) {
					trace(struct {
						TrialIndex int              `json:"trial_index"`
						Trial      Trial            `json:"trial"`
						Event      synth.TraceEvent `json:"event"`
					}{idx + 1, trial, event})
				}
				q.TokenTrace = func(event tokensynth.Event) {
					trace(struct {
						TrialIndex int              `json:"trial_index"`
						Trial      Trial            `json:"trial"`
						Event      tokensynth.Event `json:"event"`
					}{idx + 1, trial, event})
				}
			}
			ranker, resolveErr := resolve(ctx, model)
			if resolveErr != nil || ranker == nil {
				switch {
				case errors.Is(resolveErr, context.Canceled), errors.Is(ctx.Err(), context.Canceled):
					out.Result.Status = "cancelled"
				case errors.Is(resolveErr, context.DeadlineExceeded), errors.Is(ctx.Err(), context.DeadlineExceeded):
					out.Result.Status = "timeout"
				default:
					out.Result.Status = "model_error"
					out.Error = "model_unavailable"
				}
			} else if m.Engine == "token" {
				out.Result = runner.RunToken(ctx, data.token, ranker, q)
			} else {
				out.Result = runner.Run(ctx, p, ranker, q)
			}
		}
		out.ElapsedMS = float64(time.Since(start)) / float64(time.Millisecond)
		if out.Result.ErrorCode == "question_budget_exhausted" {
			stageBudgetExhausted = true
		}
		all += out.ElapsedMS
		if out.Result.Status == "success" {
			report.Successful++
			success += out.ElapsedMS
		} else {
			report.Failed++
		}
		report.Outcomes = append(report.Outcomes, out)
	}
	if report.Attempted > 0 {
		v := all / float64(report.Attempted)
		report.AllAttemptMeanMS = &v
		rate := float64(report.Successful) / float64(report.Attempted)
		report.SuccessRate = &rate
	}
	if report.Total > 0 {
		rate := float64(report.Successful) / float64(report.Total)
		report.PlannedSuccessRate = &rate
	}
	if report.Successful > 0 {
		v := success / float64(report.Successful)
		report.SuccessfulMeanMS = &v
	}
	report.Groups = groups(m, report.Outcomes)
	// 2026-11-12: 검증 집합의 성격을 모르는 경우 별도 unknown 그룹으로 남겨 성능 분모를 섞지 않는다.
	modes := map[string][]Outcome{}
	for _, out := range report.Outcomes {
		mode := out.Result.ValidationMode
		if mode == "" {
			mode = "unknown"
		}
		modes[mode] = append(modes[mode], out)
	}
	for _, mode := range []string{"exhaustive_public", "sampled_holdout", "unknown"} {
		if list, ok := modes[mode]; ok {
			for _, group := range groups(m, list) {
				if group.Total > 0 {
					group.ValidationMode = mode
					report.EvaluationGroups = append(report.EvaluationGroups, group)
				}
			}
		}
	}
	canonical, hashErr := json.Marshal(struct {
		Manifest   Manifest          `json:"manifest"`
		Inputs     map[string]string `json:"input_hashes"`
		Heldout    map[string]string `json:"heldout_hashes"`
		RunOptions RunOptions        `json:"run_options"`
		Version    string            `json:"version"`
	}{m, report.InputHashes, report.HeldoutHashes, report.RunOptions, report.ConfigHashVersion})
	if hashErr != nil {
		report.ConfigHashError = "invalid_config_serialization"
	} else {
		hash := sha256.Sum256(canonical)
		report.ConfigHash = hex.EncodeToString(hash[:])
	}
	return report
}
func groups(m Manifest, outcomes []Outcome) []Group {
	var result []Group
	for _, model := range m.Models {
		for _, strategy := range m.Strategies {
			g := Group{Engine: m.Engine, Model: model.ID, Strategy: strategy, StatusCounts: map[string]int{}}
			var all, success float64
			for _, o := range outcomes {
				if o.Trial.Model != model.ID || o.Trial.Strategy != strategy {
					continue
				}
				g.Total++
				g.StatusCounts[o.Result.Status]++
				if o.Result.Status == "not_run_cancelled" {
					g.NotRunCancelled++
					continue
				}
				if o.Result.Status == "not_run_budget" {
					g.NotRunBudget++
					continue
				}
				g.Attempted++
				all += o.ElapsedMS
				if o.Result.Status == "success" {
					g.Successful++
					success += o.ElapsedMS
				} else {
					g.Failed++
				}
				g.Calls += o.Result.Calls
				if o.Result.Usage == nil || o.Result.Usage.InputTokens == nil || o.Result.Usage.OutputTokens == nil {
					g.UnknownUsageCount++
				}
				knownUsage := o.Result.KnownUsage
				if knownUsage == nil {
					knownUsage = o.Result.Usage
				}
				if knownUsage != nil {
					if knownUsage.InputTokens != nil {
						g.KnownInputTokens += *knownUsage.InputTokens
					}
					if knownUsage.OutputTokens != nil {
						g.KnownOutputTokens += *knownUsage.OutputTokens
					}
				}
				if o.Result.EstimatedCost == nil {
					g.UnknownCostCount++
				}
				knownCost := o.Result.KnownEstimatedCost
				if knownCost == nil {
					knownCost = o.Result.EstimatedCost
				}
				if knownCost != nil {
					g.KnownCostSubtotal += *knownCost
					g.KnownCostObservations++
				}
			}
			if g.Attempted > 0 {
				v := all / float64(g.Attempted)
				g.AllAttemptMeanMS = &v
				r := float64(g.Successful) / float64(g.Attempted)
				g.SuccessRate = &r
			}
			if g.Total > 0 {
				r := float64(g.Successful) / float64(g.Total)
				g.PlannedSuccessRate = &r
			}
			if g.Successful > 0 {
				v := success / float64(g.Successful)
				g.SuccessfulMeanMS = &v
			}
			result = append(result, g)
		}
	}
	return result
}
