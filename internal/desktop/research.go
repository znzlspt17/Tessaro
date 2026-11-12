package desktop

import (
	"encoding/json"
	"fmt"
	"tessaro/internal/application"
	"tessaro/internal/compare"
	"strings"
)

type researchTrial struct {
	Split                            string
	Outcome                          compare.Outcome
	RunID, TracePath, ValidationMode string
	TraceAvailable                   bool
}

func filterResearchTrials(rows []researchTrial, split, strategy, problem, status string) []researchTrial {
	var out []researchTrial
	for _, r := range rows {
		t := r.Outcome.Trial
		if split != "all" && r.Split != split || strategy != "all" && t.Strategy != strategy || problem != "all" && t.Problem != problem {
			continue
		}
		success := r.Outcome.Result.Status == "success"
		if status == "success" && !success || status == "failure" && (success || strings.HasPrefix(r.Outcome.Result.Status, "not_run")) {
			continue
		}
		out = append(out, r)
	}
	return out
}
func researchTrialDetail(row researchTrial) string {
	o := row.Outcome
	r := o.Result
	source := r.Source
	if source == "" {
		source = "생성 코드 없음"
	}
	failure := strings.TrimSpace(strings.Join([]string{o.Error, r.ErrorCode}, " "))
	if failure == "" {
		failure = researchStatusReason(r.Status)
	}
	http := "알 수 없음"
	if r.HTTPRequests != nil {
		http = fmt.Sprint(*r.HTTPRequests)
	}
	return fmt.Sprintf("문제: %s\r\n전략: %s · 구분: %s · 반복: %d · seed: %d\r\n상태: %s · 실패 이유: %s\r\n질문: %d · HTTP 요청: %s\r\n반환 정책: %s · 검증: %s\r\n엔진: %s · batch: %d\r\n%s\r\n\r\n생성 코드\r\n%s", o.Trial.Problem, o.Trial.Strategy, row.Split, o.Trial.Repeat, o.Trial.Seed, r.Status, failure, r.Questions, http, r.ReturnPolicy, row.ValidationMode, r.Engine, r.DecisionBatchSize, researchBudgets(r.EffectiveBudgets), source)
}

func researchTrialEvents(events []application.Envelope, trial int) []application.Envelope {
	var out []application.Envelope
	for _, e := range events {
		if e.Kind != "comparison" {
			continue
		}
		var wrapper struct {
			TrialIndex *int            `json:"trial_index"`
			Event      json.RawMessage `json:"event"`
		}
		if json.Unmarshal(e.Data, &wrapper) == nil && wrapper.TrialIndex != nil && *wrapper.TrialIndex == trial && len(wrapper.Event) > 0 {
			out = append(out, e)
		}
	}
	return out
}

func researchStatusReason(status string) string {
	switch status {
	case "success":
		return "해당 없음"
	case "budget_exhausted":
		return "설정된 탐색 한도 소진"
	case "no_solution":
		return "현재 탐색 경로에서 통과 코드 찾지 못함"
	case "timeout":
		return "시간 제한 도달"
	case "cancelled", "canceled":
		return "취소됨"
	case "not_run_budget":
		return "예산 소진으로 미실행"
	case "not_run_cancelled":
		return "취소로 미실행"
	default:
		return "기록 없음"
	}
}
func researchBudgets(b map[string]any) string {
	var lines []string
	for _, v := range []struct{ key, label string }{{"max_calls", "최대 호출"}, {"max_candidates", "최대 후보"}, {"max_tokens", "최대 토큰"}, {"timeout_seconds", "제한 시간(초)"}} {
		if value, ok := b[v.key]; ok {
			lines = append(lines, fmt.Sprintf("%s: %v", v.label, value))
		}
	}
	if len(lines) == 0 {
		return "제한 조건: 기록 없음"
	}
	return strings.Join(lines, " · ")
}

func sameResearchTrial(a, b researchTrial) bool {
	return a.RunID == b.RunID && a.TracePath == b.TracePath && a.Outcome.TrialIndex == b.Outcome.TrialIndex
}
