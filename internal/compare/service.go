package compare

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"jevcancoding/internal/runner"
	"jevcancoding/internal/spec"
	"jevcancoding/internal/synth"
	"jevcancoding/internal/tokenspec"
	"jevcancoding/internal/tokensynth"
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
	Engine            string         `json:"engine"`
	Model             string         `json:"model"`
	Strategy          string         `json:"strategy"`
	Attempted         int            `json:"attempted"`
	Successful        int            `json:"successful"`
	Failed            int            `json:"failed"`
	NotRunCancelled   int            `json:"not_run_cancelled"`
	SuccessRate       *float64       `json:"success_rate"`
	AllAttemptMeanMS  *float64       `json:"all_attempt_mean_ms"`
	SuccessfulMeanMS  *float64       `json:"successful_mean_ms"`
	StatusCounts      map[string]int `json:"status_counts"`
	Calls             int            `json:"calls"`
	KnownInputTokens  int            `json:"known_input_tokens"`
	KnownOutputTokens int            `json:"known_output_tokens"`
	UnknownUsageCount int            `json:"unknown_usage_count"`
	KnownCostSubtotal float64        `json:"known_cost_subtotal"`
	UnknownCostCount  int            `json:"unknown_cost_count"`
}
type Report struct {
	Engine           string            `json:"engine"`
	Seed             int64             `json:"seed"`
	Shuffle          string            `json:"shuffle"`
	ConfigHash       string            `json:"config_hash"`
	InputHashes      map[string]string `json:"input_hashes"`
	HeldoutHashes    map[string]string `json:"heldout_hashes"`
	Total            int               `json:"total"`
	Attempted        int               `json:"attempted"`
	NotRunCancelled  int               `json:"not_run_cancelled"`
	Successful       int               `json:"successful"`
	Failed           int               `json:"failed"`
	SuccessRate      *float64          `json:"success_rate"`
	AllAttemptMeanMS *float64          `json:"all_attempt_mean_ms"`
	SuccessfulMeanMS *float64          `json:"successful_mean_ms"`
	Groups           []Group           `json:"groups"`
	Outcomes         []Outcome         `json:"outcomes"`
}
type cachedProblem struct {
	p     spec.Problem
	token tokenspec.Problem
	err   error
}

type cachedEvaluation struct {
	tests      []spec.Test
	tokenTests []tokenspec.Test
	err        error
}

// 2026-11-05: 실패를 분모에서 숨기지 않고 동일 계획을 순차 실행한다.
func Run(ctx context.Context, base string, m Manifest, trace func(any), traceCode ...bool) Report {
	if m.Engine == "" {
		m.Engine = "token"
	}
	plan := Plan(m)
	report := Report{Engine: m.Engine, Seed: m.Seed, Shuffle: "Go math/rand seeded shuffle of problems", Total: len(plan), InputHashes: map[string]string{}, HeldoutHashes: map[string]string{}}
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
		f, err := os.Open(filepath.Join(base, x.Spec))
		var p spec.Problem
		var tokenProblem tokenspec.Problem
		if err == nil {
			if m.Engine == "token" {
				tokenProblem, err = tokenspec.Decode(f)
			} else {
				p, err = spec.Decode(f)
			}
			f.Close()
		}
		cached[x.ID] = cachedProblem{p: p, token: tokenProblem, err: err}
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
	for idx, trial := range plan {
		out := Outcome{TrialIndex: idx + 1, Trial: trial}
		out.Result.Engine = m.Engine
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
			if m.Engine == "token" {
				out.Error = "invalid token specification; legacy name/params/return_type format requires engine ast"
			} else {
				out.Error = "invalid AST specification"
			}
		} else {
			q := runner.Request{Strategy: trial.Strategy, InputPrice: model.InputPrice, OutputPrice: model.OutputPrice}
			if problem.Heldout != "" && m.Engine == "ast" {
				// 2026-11-05: 첫 제출 후 읽은 평가셋을 고정해 조합 사이 파일 변경이 실험 조건을 바꾸지 못하게 한다.
				q.Heldout = func() ([]spec.Test, error) {
					evaluation, loaded := evaluations[trial.Problem]
					if !loaded {
						f, e := os.Open(filepath.Join(base, problem.Heldout))
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
						f, e := os.Open(filepath.Join(base, problem.Heldout))
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
			if m.Engine == "token" {
				out.Result = runner.RunToken(ctx, data.token, model.NewRanker(), q)
			} else {
				out.Result = runner.Run(ctx, p, model.NewRanker(), q)
			}
		}
		out.ElapsedMS = float64(time.Since(start)) / float64(time.Millisecond)
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
	if report.Successful > 0 {
		v := success / float64(report.Successful)
		report.SuccessfulMeanMS = &v
	}
	report.Groups = groups(m, report.Outcomes)
	canonical, _ := json.Marshal(struct {
		Manifest Manifest          `json:"manifest"`
		Inputs   map[string]string `json:"input_hashes"`
		Heldout  map[string]string `json:"heldout_hashes"`
	}{m, report.InputHashes, report.HeldoutHashes})
	hash := sha256.Sum256(canonical)
	report.ConfigHash = hex.EncodeToString(hash[:])
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
				g.StatusCounts[o.Result.Status]++
				if o.Result.Status == "not_run_cancelled" {
					g.NotRunCancelled++
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
				if o.Result.Usage != nil {
					if o.Result.Usage.InputTokens != nil {
						g.KnownInputTokens += *o.Result.Usage.InputTokens
					}
					if o.Result.Usage.OutputTokens != nil {
						g.KnownOutputTokens += *o.Result.Usage.OutputTokens
					}
				}
				if o.Result.EstimatedCost == nil {
					g.UnknownCostCount++
				} else {
					g.KnownCostSubtotal += *o.Result.EstimatedCost
				}
			}
			if g.Attempted > 0 {
				v := all / float64(g.Attempted)
				g.AllAttemptMeanMS = &v
				r := float64(g.Successful) / float64(g.Attempted)
				g.SuccessRate = &r
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
