package main

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"jevcancode/internal/application"
	"math"
	"math/rand"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"

	"jevcancode/internal/compare"
	"jevcancode/internal/modelprovider"
	"jevcancode/internal/rank"
)

type auditObservation struct {
	Variant        string             `json:"variant"`
	Seed           int64              `json:"seed,omitempty"`
	Choice         string             `json:"choice"`
	Scores         map[string]float64 `json:"scores"`
	Agreement      *bool              `json:"agreement,omitempty"`
	TotalVariation *float64           `json:"total_variation,omitempty"`
	Usage          *rank.Usage        `json:"usage"`
}

// 2026-11-12: 저장된 단일 토큰 질문만 재생해 교란 실험이 다른 문제나 정답을 만들어내지 않게 한다.
func audit(ctx context.Context, args []string, out, stderr io.Writer, resolve resolver) int {
	f := flags("audit", stderr)
	path := f.String("trace", "", "saved trace")
	seq := f.Uint64("seq", 0, "decision event sequence")
	limit := f.Int("max-questions", 0, "global maximum questions")
	parentLocal := f.Bool("parent-local", false, "compare joint with parent-local questions")
	if f.Parse(args) != nil || f.NArg() != 0 || *path == "" || *seq == 0 || *limit < 7 {
		return fail(stderr, "trace, seq 및 최소 7개의 질문 예산이 필요합니다")
	}
	file, e := os.Open(*path)
	if e != nil {
		return fail(stderr, "trace 읽기 실패")
	}
	defer file.Close()
	h := sha256.New()
	scanner := bufio.NewScanner(io.TeeReader(file, h))
	scanner.Buffer(make([]byte, 65536), 8<<20)
	var target struct {
		Seq   uint64 `json:"seq"`
		Kind  string `json:"kind"`
		RunID string `json:"run_id"`
		Data  struct {
			Reason          string                     `json:"reason"`
			Phase           string                     `json:"phase"`
			DecisionContext map[string]json.RawMessage `json:"decision_context"`
		} `json:"data"`
	}
	found := false
	for scanner.Scan() {
		var row struct {
			Seq  uint64          `json:"seq"`
			Kind string          `json:"kind"`
			Data json.RawMessage `json:"data"`
		}
		if json.Unmarshal(scanner.Bytes(), &row) != nil {
			return fail(stderr, "잘못된 trace")
		}
		if row.Seq == *seq {
			if found || json.Unmarshal(scanner.Bytes(), &target) != nil {
				return fail(stderr, "잘못된 trace")
			}
			found = true
		}
	}
	if scanner.Err() != nil || !found || target.Kind != "search" || target.Data.Reason != "decision_context" || target.Data.Phase != "" {
		return fail(stderr, "재생할 수 없는 단일 decision_context")
	}
	// 출처 모델이 다른 질문을 같은 모델의 재현으로 오인하지 않도록 저장 결과를 검증한다.
	id := strings.TrimSuffix(filepath.Base(*path), ".trace.jsonl")
	if id == filepath.Base(*path) || target.RunID != id {
		return fail(stderr, "출처 기록 불일치")
	}
	manager := application.New(application.Config{OutputDir: filepath.Dir(*path)})
	record, err := manager.ReadHistory(id)
	if err != nil {
		return fail(stderr, "출처 기록 없음")
	}
	var snapshot struct {
		Results []struct {
			Result struct {
				Ranker       rank.Metadata `json:"ranker"`
				ReturnPolicy string        `json:"return_policy"`
			} `json:"result"`
		} `json:"results"`
	}
	if json.Unmarshal(record.Data, &snapshot) != nil || len(snapshot.Results) != 1 || snapshot.Results[0].Result.ReturnPolicy != "values-only-v1" || snapshot.Results[0].Result.Ranker.Model != "jev-1.14.0" || snapshot.Results[0].Result.Ranker.ReturnedModel != "jev-1.14.0" {
		return fail(stderr, "출처 모델 버전 확인 불가")
	}
	state := target.Data.DecisionContext
	var exactState, transport, policy, schema string
	if json.Unmarshal(state["schema_version"], &schema) != nil || schema != "decision-context-v1" || json.Unmarshal(state["return_policy"], &policy) != nil || policy != snapshot.Results[0].Result.ReturnPolicy || json.Unmarshal(state["state_json"], &exactState) != nil || json.Unmarshal(state["transport"], &transport) != nil || transport != "single" {
		return fail(stderr, "정확한 단일 요청 상태 없음")
	}
	var instruction string
	var criteria map[string]struct {
		ParentID  string `json:"parent_id"`
		TokenKind string `json:"token_kind"`
		Lexeme    string `json:"lexeme"`
		Rule      string `json:"rule"`
	}
	var parents map[string]struct {
		Prefix string `json:"prefix"`
	}
	if json.Unmarshal(state["instructions"], &instruction) != nil || json.Unmarshal(state["criteria"], &criteria) != nil || json.Unmarshal(state["parents"], &parents) != nil || len(criteria) < 2 || len(criteria) > 255 || len(state["intent"]) == 0 || len(state["public_tests"]) == 0 {
		return fail(stderr, "불완전한 decision_context")
	}
	// 2026-11-12: 저장된 후보 배열과 실제 질문 criteria를 대조해 변조되거나 불완전한 trace를 재생하지 않는다.
	var candidates []rank.Candidate
	if json.Unmarshal(state["ordered_candidates"], &candidates) != nil || len(candidates) != len(criteria) {
		return fail(stderr, "후보 목록 불일치")
	}
	semantic := map[string]string{}
	seen := map[string]bool{}
	for _, c := range candidates {
		criterion, exists := criteria[c.ID]
		p, ok := parents[c.ParentID]
		if !exists || !ok || c.ParentID == "" || c.TokenKind == "" || criterion.ParentID != c.ParentID || criterion.TokenKind != c.TokenKind || criterion.Lexeme != c.Lexeme || criterion.Rule != c.Rule {
			return fail(stderr, "후보 목록 불일치")
		}
		key := p.Prefix + "\x00" + c.Lexeme
		if seen[key] {
			return fail(stderr, "중복된 토큰 의미")
		}
		seen[key] = true
		semantic[c.ID] = key
	}
	expected, generated, e := rank.ChoiceQuestion(candidates)
	encoded, _ := json.Marshal(generated)
	var generatedCriteria map[string]any
	var originalCriteria map[string]any
	if json.Unmarshal(encoded, &generatedCriteria) != nil || json.Unmarshal(state["criteria"], &originalCriteria) != nil || !reflect.DeepEqual(generatedCriteria, originalCriteria) || e != nil || expected != instruction {
		return fail(stderr, "지원하지 않는 질문 지시")
	}
	delete(state, "instructions")
	delete(state, "criteria")
	delete(state, "state_json")
	delete(state, "transport")
	delete(state, "schema_version")
	delete(state, "ordered_candidates")
	var savedState map[string]json.RawMessage
	if json.Unmarshal([]byte(exactState), &savedState) != nil || !reflect.DeepEqual(state, savedState) {
		return fail(stderr, "질문 상태가 일치하지 않습니다")
	}
	// 2026-11-12: HTTP criteria map은 ID순으로 직렬화되므로 이름만 바꾼 경우 wire 순서를 유지한다.
	sort.Slice(candidates, func(i, j int) bool { return candidates[i].ID < candidates[j].ID })
	// 2026-11-12: 전체 실험에 필요한 질문을 모델 준비 전에 계산해 부분적인 유료 실행을 막는다.
	groups := map[string][]rank.Candidate{}
	parentIDs := []string{}
	if *parentLocal {
		for _, c := range candidates {
			if _, ok := groups[c.ParentID]; !ok {
				parentIDs = append(parentIDs, c.ParentID)
			}
			groups[c.ParentID] = append(groups[c.ParentID], c)
		}
		sort.Strings(parentIDs)
		needed := 7
		for _, id := range parentIDs {
			if len(groups[id]) > 1 {
				needed++
			}
		}
		if *limit < needed {
			return fail(stderr, "부모별 비교에 필요한 질문 예산 부족")
		}
	}
	raw := []byte(exactState)
	var provider *modelprovider.Provider
	if resolve == nil {
		provider = modelprovider.New(modelprovider.Config{})
		resolve = provider.Resolve
		defer provider.Close()
	}
	model := compare.Model{ID: "jev", Ranker: "jev", Model: "jev-1.14.0", KeyEnv: "TYPESAFE_API_KEY"}
	r, e := resolve(ctx, model)
	if e != nil {
		return fail(stderr, "모델 공급자 오류")
	}
	g := (&questionGate{limit: *limit}).wrap(r)
	dr, ok := g.(rank.DecisionRanker)
	if !ok {
		return fail(stderr, "결정 응답 미지원")
	}
	if c, ok := g.(rank.CategoricalRanker); !ok || !c.NormalizedCategorical() {
		return fail(stderr, "정규화 분포 미지원")
	}
	observations := []auditObservation{}
	parentResults := []map[string]any{}
	attempts := 0
	var latestUsage *rank.Usage
	var initialHTTP int64
	if counter, ok := g.(rank.HTTPRequestCounter); ok {
		initialHTTP = counter.HTTPRequests()
	}
	// 2026-11-12: 실패한 요청 이후에도 이미 지불된 응답과 사용량을 손실하지 않도록 부분 결과를 반환한다.
	finish := func(status, code string) int {
		completed := len(observations)
		for _, parent := range parentResults {
			if forced, ok := parent["forced"].(bool); ok && !forced {
				completed++
			}
		}
		result := map[string]any{"status": status, "error_code": code, "trace_sha256": hex.EncodeToString(h.Sum(nil)), "seq": *seq, "model": "jev-1.14.0", "return_policy": policy, "observations": observations, "parent_local": parentResults, "completed": completed, "questions": attempts, "latest_usage": latestUsage}
		if counter, ok := g.(rank.HTTPRequestCounter); ok {
			result["http_requests"] = counter.HTTPRequests() - initialHTTP
		} else {
			result["http_requests"] = nil
		}
		if encode(out, result) != nil {
			return 1
		}
		if status != "completed" {
			return 1
		}
		return 0
	}
	var baseline map[string]float64
	var first string
	for variant := 0; variant < 7; variant++ {
		cs := append([]rank.Candidate(nil), candidates...)
		mapping := map[string]string{}
		kind := "baseline"
		var seed int64
		if variant == 0 {
			for _, c := range cs {
				mapping[c.ID] = semantic[c.ID]
			}
		} else {
			seed = int64(42 + (variant-1)%3)
			kind = "id-rename"
			if variant > 3 {
				kind = "id-and-wire-order"
				rng := rand.New(rand.NewSource(seed))
				rng.Shuffle(len(cs), func(i, j int) { cs[i], cs[j] = cs[j], cs[i] })
			}
			for i := range cs {
				old := cs[i].ID
				cs[i].ID = fmt.Sprintf("v%d_%03d", seed, i)
				mapping[cs[i].ID] = semantic[old]
			}
		}
		attempts++
		d, err := dr.RankDecision(ctx, string(raw), cs)
		latestUsage = d.Usage
		if err != nil {
			return finish("failed", "question_failed")
		}
		if provider, ok := r.(rank.MetadataProvider); !ok || provider.Metadata().Model != "jev-1.14.0" || provider.Metadata().ReturnedModel != "jev-1.14.0" {
			return finish("failed", "model_mismatch")
		}
		if rank.Validate(cs, d.Scores) != nil || mapping[d.ChosenID] == "" {
			return finish("failed", "invalid_decision")
		}
		scores := map[string]float64{}
		sum := 0.0
		for _, v := range d.Scores {
			if v.Score < 0 || v.Score > 1 || math.IsNaN(v.Score) {
				return finish("failed", "invalid_probability")
			}
			scores[mapping[v.ID]] = v.Score
			sum += v.Score
		}
		if math.Abs(sum-1) > math.Max(0.0001, float64(len(cs))*0.000051) || sum <= 0 {
			return finish("failed", "invalid_probability_sum")
		}
		for key := range scores {
			scores[key] /= sum
		}
		obs := auditObservation{Variant: kind, Seed: seed, Choice: mapping[d.ChosenID], Scores: scores, Usage: d.Usage}
		if variant == 0 {
			baseline = scores
			first = obs.Choice
		} else {
			agree := first == obs.Choice
			tv := 0.0
			for k, v := range baseline {
				tv += math.Abs(v - scores[k])
			}
			tv /= 2
			obs.Agreement = &agree
			obs.TotalVariation = &tv
		}
		observations = append(observations, obs)
	}
	// 2026-11-12: joint 분포의 각 부모 내부 조건부만 비교해 서로 다른 부모의 점수를 결합하지 않는다.
	if *parentLocal {
		var sourceParents map[string]json.RawMessage
		if json.Unmarshal(savedState["parents"], &sourceParents) != nil {
			return finish("failed", "invalid_parent_state")
		}
		for _, id := range parentIDs {
			group := groups[id]
			mass := 0.0
			for _, candidate := range group {
				mass += baseline[semantic[candidate.ID]]
			}
			item := map[string]any{"parent_id": id, "parent_prefix": parents[id].Prefix, "joint_mass": mass, "forced": len(group) == 1, "total_variation": nil, "agreement": nil, "joint_conditional_choice": nil, "local_choice": nil, "usage": nil}
			local := map[string]float64{}
			localChoice := ""
			if len(group) == 1 {
				local[semantic[group[0].ID]] = 1
				localChoice = semantic[group[0].ID]
			} else {
				localState := make(map[string]json.RawMessage, len(savedState))
				for k, v := range savedState {
					localState[k] = v
				}
				parentData, e := json.Marshal(map[string]json.RawMessage{id: sourceParents[id]})
				if e != nil {
					return finish("failed", "invalid_parent_state")
				}
				localState["parents"] = parentData
				encoded, e := json.Marshal(localState)
				if e != nil {
					return finish("failed", "invalid_parent_state")
				}
				attempts++
				d, e := dr.RankDecision(ctx, string(encoded), group)
				latestUsage = d.Usage
				if e != nil {
					return finish("failed", "question_failed")
				}
				if provider, ok := r.(rank.MetadataProvider); !ok || provider.Metadata().Model != "jev-1.14.0" || provider.Metadata().ReturnedModel != "jev-1.14.0" {
					return finish("failed", "model_mismatch")
				}
				if rank.Validate(group, d.Scores) != nil || semantic[d.ChosenID] == "" {
					return finish("failed", "invalid_decision")
				}
				total := 0.0
				for _, score := range d.Scores {
					if score.Score < 0 || score.Score > 1 || math.IsNaN(score.Score) {
						return finish("failed", "invalid_probability")
					}
					local[semantic[score.ID]] = score.Score
					total += score.Score
				}
				if total <= 0 || math.Abs(total-1) > math.Max(.0001, float64(len(group))*.000051) {
					return finish("failed", "invalid_probability_sum")
				}
				for k := range local {
					local[k] /= total
				}
				localChoice = semantic[d.ChosenID]
				item["usage"] = d.Usage
			}
			item["local_choice"] = localChoice
			item["local_distribution"] = local
			if mass > 0 {
				joint := map[string]float64{}
				max := -1.0
				best := ""
				ties := 0
				tv := 0.0
				for _, candidate := range group {
					key := semantic[candidate.ID]
					v := baseline[key] / mass
					joint[key] = v
					tv += math.Abs(v - local[key])
					if v > max {
						max = v
						best = key
						ties = 1
					} else if v == max {
						ties++
					}
				}
				tv /= 2
				item["joint_conditional_distribution"] = joint
				item["total_variation"] = tv
				if ties == 1 {
					item["joint_conditional_choice"] = best
					item["agreement"] = best == localChoice
				} else if first != "" && joint[first] == max {
					item["joint_conditional_choice"] = first
					item["agreement"] = first == localChoice
				}
			}
			parentResults = append(parentResults, item)
		}
	}
	return finish("completed", "")
}
