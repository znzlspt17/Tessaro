package desktop

import (
	"encoding/json"
	"jevcancode/internal/application"
	"jevcancode/internal/compare"
	"jevcancode/internal/runner"
	"strings"
	"testing"
)

func TestResearchTrialFilterAndDetails(t *testing.T) {
	rows := []researchTrial{
		{Split: "dev", Outcome: compare.Outcome{Trial: compare.Trial{Strategy: "beam", Problem: "p", Seed: 42}, Result: runner.Result{Status: "success", Source: "return x", Questions: 3}}},
		{Split: "eval", Outcome: compare.Outcome{Trial: compare.Trial{Strategy: "lds", Problem: "q", Seed: 43}, Error: "model_unavailable", Result: runner.Result{Status: "failed", ErrorCode: "failure"}}},
	}
	got := filterResearchTrials(rows, "dev", "beam", "p", "success")
	if len(got) != 1 || got[0].Outcome.Trial.Seed != 42 {
		t.Fatalf("filtered = %+v", got)
	}
	if got := filterResearchTrials(rows, "all", "all", "all", "failure"); len(got) != 1 || got[0].Outcome.Error != "model_unavailable" {
		t.Fatalf("failures = %+v", got)
	}
	detail := researchTrialDetail(rows[1])
	for _, want := range []string{"q", "43", "model_unavailable", "failure"} {
		if !strings.Contains(detail, want) {
			t.Errorf("detail missing %q: %s", want, detail)
		}
	}
}

func TestResearchTraceFiltersExactTrial(t *testing.T) {
	events := []application.Envelope{
		{Kind: "comparison", Data: json.RawMessage(`{"trial_index":1,"event":{"kind":"x"}}`)},
		{Kind: "comparison", Data: json.RawMessage(`{"trial_index":2,"event":{"kind":"x"}}`)},
		{Kind: "comparison", Data: json.RawMessage(`{"event":{"kind":"x"}}`)},
		{Kind: "report", Data: json.RawMessage(`{"trial_index":2}`)},
	}
	got := researchTrialEvents(events, 2)
	if len(got) != 1 || !strings.Contains(string(got[0].Data), `"trial_index":2`) {
		t.Fatalf("filtered events = %+v", got)
	}
}

func TestResearchFailureReasonAndConditions(t *testing.T) {
	row := researchTrial{Outcome: compare.Outcome{Result: runner.Result{Status: "budget_exhausted", EffectiveBudgets: map[string]any{"max_calls": 64, "max_candidates": 8192}}}}
	got := researchTrialDetail(row)
	for _, want := range []string{"설정된 탐색 한도 소진", "최대 호출: 64", "최대 후보: 8192"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in %s", want, got)
		}
	}
	for status, want := range map[string]string{"no_solution": "현재 탐색 경로에서 통과 코드 찾지 못함", "timeout": "시간 제한", "cancelled": "취소", "not_run_budget": "미실행"} {
		row.Outcome.Result.Status = status
		if got := researchTrialDetail(row); !strings.Contains(got, want) {
			t.Errorf("%s missing %s: %s", status, want, got)
		}
	}
}

func TestResearchTrialIdentityIncludesShardPath(t *testing.T) {
	a := researchTrial{RunID: "2026-12-11.001", TracePath: `C:\study\shard-1\2026-12-11.001.trace.jsonl`, Outcome: compare.Outcome{TrialIndex: 1}}
	b := a
	b.TracePath = `C:\study\shard-2\2026-12-11.001.trace.jsonl`
	if sameResearchTrial(a, b) {
		t.Fatal("different shard paths were accepted as the same trial")
	}
	if !sameResearchTrial(a, a) {
		t.Fatal("same trial was rejected")
	}
}
