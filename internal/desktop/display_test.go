package desktop

import (
	"encoding/json"
	"jevcancode/internal/application"
	"strings"
	"testing"
)

func TestReadableDecisionContextAndCandidate(t *testing.T) {
	score := .75
	ctx := map[string]any{"intent": "팩토리얼", "inputs": []any{"n"}, "type_hints": map[string]any{"n": "int"}, "output_type_hint": "int", "literals": map[string]any{"integers": []any{float64(0), float64(1)}}, "public_tests": []any{map[string]any{"inputs": map[string]any{"n": float64(3)}, "want": float64(6)}}, "parents": map[string]any{"a": map[string]any{"prefix": "func f(n int) int {", "visible_symbols": map[string]any{"n": true}}}, "instructions": "choose", "criteria": map[string]any{"c": map[string]any{"parent_id": "a", "lexeme": "n", "token_kind": "IDENT", "rule": "identifier"}}}
	got := formatDecisionContext(ctx)
	for _, s := range []string{"요구사항: 팩토리얼", "n: int", "반환 타입: int", "n = 3", "기대 결과: 6", "func f(n int) int {", "선택 지시문: choose", "c", "identifier"} {
		if !strings.Contains(got, s) {
			t.Errorf("missing %q: %s", s, got)
		}
	}
	if strings.Contains(got, `"intent"`) {
		t.Errorf("JSON in output: %s", got)
	}
	row := formatCandidate(application.CandidateItem{ID: "c", ParentID: "a", Lexeme: "n", Rank: 1, Score: &score, Reason: "pruned"}, ctx)
	for _, s := range []string{"c", "a", "n", "0.75", "전달", "제외"} {
		if !strings.Contains(row, s) {
			t.Errorf("missing %q: %s", s, row)
		}
	}
	if !strings.Contains(formatDecisionContext(map[string]any{}), "기록 없음") {
		t.Fatal("old trace must report missing instructions and candidates")
	}
}

func TestReadableProblemAndResult(t *testing.T) {
	p := &application.PublicProblem{ID: "f", Intent: "팩토리얼", PublicTests: []any{map[string]any{"inputs": map[string]any{"n": float64(3)}, "want": float64(6)}}}
	if got := formatProblem(p); !strings.Contains(got, "n = 3") || !strings.Contains(got, "기대 결과: 6") {
		t.Fatalf("problem: %s", got)
	}
	if got := formatResult(application.Snapshot{}); !strings.Contains(got, "아직 결과 없음") {
		t.Fatalf("result: %s", got)
	}
	if got := formatResult(application.Snapshot{Payload: json.RawMessage(`{"status":"timeout","calls":2}`)}); !strings.Contains(got, "timeout") || !strings.Contains(got, "2") || strings.Contains(got, `"status"`) {
		t.Fatalf("result: %s", got)
	}
}

func TestReadableRejectedCandidateShowsReasonAndTransmission(t *testing.T) {
	c := application.CandidateItem{ID: "r1", ParentID: "p", Lexeme: "(", TokenKind: "(", Reason: "body_rejected", RejectionReason: "non_callable"}
	ctx := map[string]any{"criteria": map[string]any{"c1": map[string]any{"parent_id": "p"}}}
	got := formatCandidate(c, ctx)
	for _, part := range []string{"r1", "상위 경로: p", "토큰: (", "모델에 전달되지 않음", "본문 검사 제외", "함수로 호출할 수 없는"} {
		if !strings.Contains(got, part) {
			t.Errorf("missing %q: %s", part, got)
		}
	}
	if got = formatCandidate(c, nil); !strings.Contains(got, "전달 여부: 기록 없음") {
		t.Fatalf("old record inferred transmission: %s", got)
	}
}

func TestIrrecoverableExpressionRejectionLiveAndHistory(t *testing.T) {
	const reason = "irrecoverable_expression_statement"
	c := application.CandidateItem{ID: "r1", ParentID: "p", Lexeme: "+", Reason: "body_rejected", RejectionReason: reason}
	if got := formatCandidate(c, nil); !strings.Contains(got, "완성 불가능한 문장") {
		t.Fatalf("live candidate: %s", got)
	}
	events := []TraceEvent{{Seq: 1, RunID: "r", Kind: "search", Data: []byte(`{"id":"r1","parent_id":"p","lexeme":"+","reason":"body_rejected","rejection_reason":"irrecoverable_expression_statement","step":1}`)}}
	frames := Reconstruct(events)
	var snapshot application.Snapshot
	if len(frames) != 1 {
		t.Fatalf("history frames: %+v", frames)
	}
	if err := json.Unmarshal([]byte(frames[0].Text), &snapshot); err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Candidates) != 1 {
		t.Fatalf("history candidates: %+v", snapshot.Candidates)
	}
	if got := formatCandidate(snapshot.Candidates[0], snapshot.DecisionContext); !strings.Contains(got, "완성 불가능한 문장") {
		t.Fatalf("history candidate: %s", got)
	}
	if got := formatCandidate(application.CandidateItem{Reason: "body_rejected", RejectionReason: "unused_expression"}, nil); !strings.Contains(got, "독립된 산술식") {
		t.Fatalf("legacy candidate: %s", got)
	}
}

func TestBoolOrderRejectionIsReadable(t *testing.T) {
	c := application.CandidateItem{ID: "r1", Lexeme: "<", Reason: "body_rejected", RejectionReason: "bool_order_comparison"}
	got := formatCandidate(c, nil)
	if !strings.Contains(got, "참·거짓 값에는 크기 비교를 할 수 없습니다") || !strings.Contains(got, "토큰: <") {
		t.Fatalf("candidate: %s", got)
	}
	if strings.Contains(got, "번째 토큰") {
		t.Fatalf("old record invented position: %s", got)
	}
}

func TestBoolOrderPositionLiveAndHistory(t *testing.T) {
	const live = `{"id":"r1","lexeme":"<","reason":"body_rejected","rejection_reason":"bool_order_comparison","rejection_token_index":0}`
	var c application.CandidateItem
	if err := json.Unmarshal([]byte(live), &c); err != nil {
		t.Fatal(err)
	}
	if got := formatCandidate(c, nil); !strings.Contains(got, "1번째 토큰") {
		t.Fatalf("live: %s", got)
	}
	events := []TraceEvent{{Seq: 1, RunID: "r", Kind: "search", Data: []byte(`{"step":1,"id":"r1","lexeme":"<","reason":"body_rejected","rejection_reason":"bool_order_comparison","rejection_token_index":4}`)}}
	frames := Reconstruct(events)
	var s application.Snapshot
	if err := json.Unmarshal([]byte(frames[0].Text), &s); err != nil {
		t.Fatal(err)
	}
	if len(s.Candidates) != 1 {
		t.Fatalf("history candidates: %+v", s.Candidates)
	}
	if got := formatCandidate(s.Candidates[0], nil); !strings.Contains(got, "5번째 토큰") || !strings.Contains(got, "토큰: <") {
		t.Fatalf("history: %s", got)
	}
}

func TestReadableLargeIntegerKeepsExactValue(t *testing.T) {
	p := &application.PublicProblem{Intent: "정수", PublicTests: []any{map[string]any{"inputs": map[string]any{"n": int64(9007199254740993)}, "want": int64(9007199254740993)}}}
	if got := formatProblem(p); strings.Count(got, "9007199254740993") != 2 {
		t.Fatalf("integer changed: %s", got)
	}
	if got := formatResult(application.Snapshot{Payload: json.RawMessage(`{"value":9007199254740993}`)}); !strings.Contains(got, "9007199254740993") {
		t.Fatalf("result integer changed: %s", got)
	}
}

func TestDecisionContextExplainsVisibleNames(t *testing.T) {
	ctx := map[string]any{"parents": map[string]any{"p": map[string]any{"prefix": "func f(xs []int) {", "visible_symbols": map[string]any{"xs": true}}}}
	got := formatDecisionContext(ctx)
	if !strings.Contains(got, "사용 가능한 이름: xs") || strings.Contains(got, "xs = true") {
		t.Fatalf("visible names should be readable: %s", got)
	}
}

func TestHypothesisResultPrefersSummary(t *testing.T) {
	s := application.Snapshot{JobKind: "hypothesis", Payload: json.RawMessage(`{"summary":"요약 보고서","problems":[{"details":"긴 세부 내용"}]}`)}
	got := formatResult(s)
	if got != "요약 보고서" {
		t.Fatalf("result = %q", got)
	}
}
