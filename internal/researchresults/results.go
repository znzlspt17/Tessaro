package researchresults

import (
	"encoding/json"
	"fmt"
	"io"
	"jevcancode/internal/compare"
	"os"
	"path/filepath"
	"reflect"
	"strings"
)

type Protocol struct {
	Kind              string   `json:"kind"`
	Problems          int      `json:"problems"`
	Strategies        []string `json:"strategies"`
	Repeats           int      `json:"repeats"`
	Seeds             []int64  `json:"seeds"`
	PlannedTrials     int      `json:"planned_trials"`
	Shards            int      `json:"shards"`
	ProblemsPerShard  int      `json:"problems_per_shard"`
	MaxCalls          int      `json:"max_calls_per_trial"`
	MaxCandidates     int      `json:"max_candidates"`
	MaxTokens         int      `json:"max_tokens"`
	TimeoutSeconds    int      `json:"timeout_seconds"`
	DecisionBatchSize int      `json:"decision_batch_size"`
	ReturnPolicy      string   `json:"return_policy"`
	Model             string   `json:"model"`
}
type Metrics struct {
	Planned, Observed, Attempted, Success, NotRun int
	StatusCounts                                  map[string]int
	Questions                                     int
	HTTPRequests                                  int64
	HTTPUnknown                                   int
	KnownInputTokens, KnownOutputTokens           int64
	UnknownUsage                                  int
	KnownCost                                     float64
	KnownCostObservations, UnknownCost            int
}
type StrategySummary struct {
	Strategy       string
	All, Dev, Eval Metrics
}
type Trial struct {
	Outcome                      compare.Outcome
	Shard                        int
	RunID, RecordPath, TracePath string
	TraceAvailable               bool
	Split, ValidationMode        string
}
type Study struct {
	Protocol   Protocol
	Strategies []StrategySummary
	Trials     []Trial
}
type config struct {
	shards, problemsPerShard, repeats, dev, eval int
	strategies                                   []string
	seeds                                        []int64
}
type shardSummary struct {
	Status string         `json:"status"`
	RunID  string         `json:"run_id"`
	Report compare.Report `json:"report"`
}
type persisted struct {
	RunID   string         `json:"run_id"`
	Status  string         `json:"status"`
	JobKind string         `json:"job_kind"`
	Payload compare.Report `json:"payload"`
}

func Load(dir string) (Study, error) {
	return load(dir, config{shards: 12, problemsPerShard: 5, repeats: 3, dev: 40, eval: 20, strategies: []string{"strict-greedy", "beam", "lds", "levin", "phs", "mcts"}, seeds: []int64{42, 43, 44}})
}
func readJSON(path string, v any) error {
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("missing %s", filepath.Base(path))
		}
		return err
	}
	defer f.Close()
	d := json.NewDecoder(f)
	if err = d.Decode(v); err != nil {
		return fmt.Errorf("invalid JSON %s: %w", filepath.Base(path), err)
	}
	var extra any
	if err = d.Decode(&extra); err != io.EOF {
		return fmt.Errorf("invalid JSON trailing data: %s", filepath.Base(path))
	}
	return nil
}
func sameSeed(a []int64, b []int64) bool { return reflect.DeepEqual(a, b) }
func splitOf(p compare.Problem) (string, string, error) {
	path := strings.ReplaceAll(p.Spec, "\\", "/")
	parts := strings.Split(path, "/")
	if len(parts) < 3 || parts[len(parts)-2] != "specs" || parts[len(parts)-1] != p.ID+".json" {
		return "", "", fmt.Errorf("invalid spec path for %s", p.ID)
	}
	split := parts[len(parts)-3]
	if split != "dev" && split != "eval" {
		return "", "", fmt.Errorf("invalid split for %s", p.ID)
	}
	mode := "exhaustive_public"
	if p.Heldout != "" {
		held := strings.Split(strings.ReplaceAll(p.Heldout, "\\", "/"), "/")
		if len(held) < 3 || held[len(held)-3] != split || held[len(held)-2] != "heldout" || held[len(held)-1] != p.ID+".json" {
			return "", "", fmt.Errorf("invalid heldout path for %s", p.ID)
		}
		mode = "sampled_holdout"
	}
	return split, mode, nil
}
func add(m *Metrics, o compare.Outcome) {
	m.Observed++
	status := o.Result.Status
	m.StatusCounts[status]++
	if status == "not_run_budget" || status == "not_run_cancelled" {
		m.NotRun++
		return
	}
	m.Attempted++
	if status == "success" {
		m.Success++
	}
	m.Questions += o.Result.Questions
	if o.Result.HTTPRequests == nil {
		m.HTTPUnknown++
	} else {
		m.HTTPRequests += *o.Result.HTTPRequests
	}
	usage := o.Result.Usage
	if usage == nil || usage.InputTokens == nil || usage.OutputTokens == nil {
		m.UnknownUsage++
	}
	known := o.Result.KnownUsage
	if known == nil {
		known = usage
	}
	if known != nil {
		if known.InputTokens != nil {
			m.KnownInputTokens += int64(*known.InputTokens)
		}
		if known.OutputTokens != nil {
			m.KnownOutputTokens += int64(*known.OutputTokens)
		}
	}
	if o.Result.EstimatedCost == nil {
		m.UnknownCost++
	}
	cost := o.Result.KnownEstimatedCost
	if cost == nil {
		cost = o.Result.EstimatedCost
	}
	if cost != nil {
		m.KnownCost += *cost
		m.KnownCostObservations++
	}
}
func emptyMetrics(planned int) Metrics {
	return Metrics{Planned: planned, StatusCounts: map[string]int{}}
}

// 2026-11-12: 원본 요약과 저장 기록을 함께 검증하고 trace는 지연 로드를 위해 경로만 확인한다.
func load(dir string, c config) (Study, error) {
	var study Study
	absolute, err := filepath.Abs(dir)
	if err != nil {
		return study, err
	}
	if err = readJSON(filepath.Join(absolute, "protocol.json"), &study.Protocol); err != nil {
		return study, err
	}
	p := study.Protocol
	if p.Kind != "fixed-final-study" || p.Problems != c.dev+c.eval || p.Shards != c.shards || p.ProblemsPerShard != c.problemsPerShard || p.Repeats != c.repeats || p.PlannedTrials != (c.dev+c.eval)*len(c.strategies)*c.repeats || !reflect.DeepEqual(p.Strategies, c.strategies) || !sameSeed(p.Seeds, c.seeds) {
		return study, fmt.Errorf("protocol plan mismatch")
	}
	if p.ReturnPolicy != "values-only-v1" || p.Model != "jev-1.14.0" || p.DecisionBatchSize != 1 || p.MaxCalls != 64 || p.MaxCandidates != 8192 || p.MaxTokens != 256 || p.TimeoutSeconds != 300 {
		return study, fmt.Errorf("protocol policy or budget mismatch")
	}
	byStrategy := map[string]int{}
	for i, name := range c.strategies {
		byStrategy[name] = i
		study.Strategies = append(study.Strategies, StrategySummary{Strategy: name, All: emptyMetrics((c.dev + c.eval) * c.repeats), Dev: emptyMetrics(c.dev * c.repeats), Eval: emptyMetrics(c.eval * c.repeats)})
	}
	seen := map[string]bool{}
	seenProblems := map[string]bool{}
	inputHashes := map[string]string{}
	heldoutHashes := map[string]string{}
	splitCounts := map[string]int{}
	for shard := 0; shard < c.shards; shard++ {
		prefix := fmt.Sprintf("shard-%02d", shard)
		var manifest compare.Manifest
		if err = readJSON(filepath.Join(absolute, prefix+".manifest.json"), &manifest); err != nil {
			return Study{}, err
		}
		if manifest.Engine != "token" || len(manifest.Problems) != c.problemsPerShard || len(manifest.Models) != 1 || manifest.Models[0].ID != "jev" || manifest.Models[0].Ranker != "jev" || manifest.Models[0].Model != p.Model || manifest.Ordering != "paired-cross-over-v1" || manifest.Repeats != c.repeats || manifest.Seed != 42 || !reflect.DeepEqual(manifest.Strategies, c.strategies) {
			return Study{}, fmt.Errorf("%s manifest condition mismatch", prefix)
		}
		model := manifest.Models[0]
		// 2026-11-12: 서로 다른 가격 단위를 합산하지 않도록 명시된 가격 조건을 확인한다.
		if model.InputPrice != nil && *model.InputPrice != 0.042 || model.OutputPrice != nil && *model.OutputPrice != 0 {
			return Study{}, fmt.Errorf("%s model price mismatch", prefix)
		}
		if model.KeyEnv != "" && model.KeyEnv != "TYPESAFE_API_KEY" {
			return Study{}, fmt.Errorf("%s model key environment name mismatch", prefix)
		}
		problems := map[string]compare.Problem{}
		splits := map[string]string{}
		modes := map[string]string{}
		for _, problem := range manifest.Problems {
			if problem.ID == "" || seenProblems[problem.ID] {
				return Study{}, fmt.Errorf("duplicate problem %s", problem.ID)
			}
			split, mode, e := splitOf(problem)
			if e != nil {
				return Study{}, e
			}
			seenProblems[problem.ID] = true
			splitCounts[split]++
			problems[problem.ID] = problem
			splits[problem.ID] = split
			modes[problem.ID] = mode
		}
		var summary shardSummary
		if err = readJSON(filepath.Join(absolute, prefix+".summary.json"), &summary); err != nil {
			return Study{}, err
		}
		if summary.Status != "completed" {
			return Study{}, fmt.Errorf("incomplete %s", prefix)
		}
		run := summary.RunID
		if run == "" || run == "." || run == ".." || filepath.Base(run) != run || strings.ContainsAny(run, "/\\:") {
			return Study{}, fmt.Errorf("invalid run id %s", prefix)
		}
		report := summary.Report
		need := c.problemsPerShard * len(c.strategies) * c.repeats
		if report.Engine != "token" || report.Seed != 42 || report.Total != need || len(report.Outcomes) != need {
			return Study{}, fmt.Errorf("missing or incomplete outcomes in %s", prefix)
		}
		o := report.RunOptions
		if o.ReturnPolicy != p.ReturnPolicy {
			return Study{}, fmt.Errorf("policy mismatch in %s", prefix)
		}
		if o.DecisionBatchSize != p.DecisionBatchSize || o.MaxTokens != p.MaxTokens || o.MaxCalls != p.MaxCalls || o.MaxCandidates != p.MaxCandidates || o.TimeoutSeconds != float64(p.TimeoutSeconds) || o.TrialSeedBase != 42 {
			return Study{}, fmt.Errorf("budget or seed mismatch in %s", prefix)
		}
		trace := filepath.Join(absolute, prefix, run+".trace.jsonl")
		available := false
		if info, e := os.Stat(trace); e == nil {
			if !info.Mode().IsRegular() {
				return Study{}, fmt.Errorf("invalid trace in %s", prefix)
			}
			available = true
		} else if !os.IsNotExist(e) {
			return Study{}, e
		}
		indices := map[int]bool{}
		for _, out := range report.Outcomes {
			t := out.Trial
			_, ok := problems[t.Problem]
			pos, known := byStrategy[t.Strategy]
			if !ok || !known || t.Engine != "token" || t.Model != "jev" || t.Repeat < 1 || t.Repeat > len(c.seeds) || t.Seed != c.seeds[t.Repeat-1] {
				return Study{}, fmt.Errorf("trial condition mismatch in %s", prefix)
			}
			key := fmt.Sprintf("%s\x00%s\x00%d", t.Problem, t.Strategy, t.Seed)
			if seen[key] {
				return Study{}, fmt.Errorf("duplicate trial in %s", prefix)
			}
			seen[key] = true
			if out.TrialIndex < 1 || out.TrialIndex > need || indices[out.TrialIndex] {
				return Study{}, fmt.Errorf("duplicate or invalid trial index in %s", prefix)
			}
			indices[out.TrialIndex] = true
			status := out.Result.Status
			if status == "" {
				return Study{}, fmt.Errorf("missing status in %s", prefix)
			}
			mode := modes[t.Problem]
			// 2026-11-12: 성공 집계는 공개 검증과 해당하는 heldout 통과 증거가 함께 있어야 한다.
			if status == "success" && (!out.Result.PublicPassed || mode == "sampled_holdout" && (out.Result.HeldoutPassed == nil || !*out.Result.HeldoutPassed)) {
				return Study{}, fmt.Errorf("inconsistent success in %s", prefix)
			}
			if out.Result.ValidationMode != "" && out.Result.ValidationMode != mode {
				return Study{}, fmt.Errorf("validation mode mismatch in %s", prefix)
			}
			if status != "not_run_budget" && status != "not_run_cancelled" {
				r := out.Result
				// 2026-11-12: 결과별 유효 설정과 동일 문제의 해시 일관성을 검증하되 원본 입력 해시와 혼동하지 않는다.
				for key, expected := range map[string]float64{"max_calls": 64, "max_candidates": 8192, "max_tokens": 256, "timeout_seconds": 300, "max_locals": 8, "max_statements": 32, "max_nesting": 4, "compile_timeout_seconds": 30, "execution_timeout_seconds": 2} {
					if raw, present := r.EffectiveBudgets[key]; present {
						value, ok := raw.(float64)
						if !ok || value != expected {
							return Study{}, fmt.Errorf("effective budget mismatch in %s", prefix)
						}
					}
				}
				if r.InputHash != "" {
					if previous := inputHashes[t.Problem]; previous != "" && previous != r.InputHash {
						return Study{}, fmt.Errorf("effective input hash mismatch in %s", prefix)
					}
					inputHashes[t.Problem] = r.InputHash
				}
				if r.HeldoutHash != "" {
					if previous := heldoutHashes[t.Problem]; previous != "" && previous != r.HeldoutHash {
						return Study{}, fmt.Errorf("heldout hash mismatch in %s", prefix)
					}
					heldoutHashes[t.Problem] = r.HeldoutHash
				}
				if r.Strategy != "" && r.Strategy != t.Strategy || r.ReturnPolicy != "" && r.ReturnPolicy != p.ReturnPolicy || r.Seed != 0 && r.Seed != t.Seed || r.DecisionBatchSize != 0 && r.DecisionBatchSize != p.DecisionBatchSize {
					return Study{}, fmt.Errorf("result condition mismatch in %s", prefix)
				}
			}
			recordPath := filepath.Join(absolute, prefix, run)
			study.Trials = append(study.Trials, Trial{Outcome: out, Shard: shard, RunID: run, RecordPath: recordPath, TracePath: trace, TraceAvailable: available, Split: splits[t.Problem], ValidationMode: mode})
			g := &study.Strategies[pos]
			add(&g.All, out)
			if splits[t.Problem] == "dev" {
				add(&g.Dev, out)
			} else {
				add(&g.Eval, out)
			}
		}
		var record persisted
		if err = readJSON(filepath.Join(absolute, prefix, run), &record); err != nil {
			return Study{}, err
		}
		if record.RunID != run || record.Status != "completed" || record.JobKind != "compare" || !reflect.DeepEqual(record.Payload, report) {
			return Study{}, fmt.Errorf("persisted record mismatch in %s", prefix)
		}
	}
	if len(seenProblems) != c.dev+c.eval || splitCounts["dev"] != c.dev || splitCounts["eval"] != c.eval || len(study.Trials) != p.PlannedTrials {
		return Study{}, fmt.Errorf("missing planned results")
	}
	return study, nil
}
