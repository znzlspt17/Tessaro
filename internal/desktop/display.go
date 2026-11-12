package desktop

import (
	"bytes"
	"encoding/json"
	"fmt"
	"jevcancode/internal/application"
	"sort"
	"strconv"
	"strings"
)

// 2026-11-07: 기록과 실시간 값의 Go 타입 차이를 없애 실제 보관된 값만 표시한다.
func displayValue(v any) any {
	b, err := json.Marshal(v)
	if err != nil {
		return nil
	}
	var result any
	if decodeDisplayJSON(b, &result) != nil {
		return nil
	}
	return result
}

func decodeDisplayJSON(b []byte, v any) error {
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.UseNumber()
	return dec.Decode(v)
}
func displayMap(v any) map[string]any { m, _ := displayValue(v).(map[string]any); return m }
func scalar(v any) string {
	if v == nil {
		return "기록 없음"
	}
	switch x := v.(type) {
	case string:
		if x == "" {
			return `""`
		}
		return x
	case bool:
		return strconv.FormatBool(x)
	case float64:
		return strconv.FormatFloat(x, 'f', -1, 64)
	case json.Number:
		return x.String()
	case []any:
		a := make([]string, 0, len(x))
		for _, v := range x {
			a = append(a, scalar(v))
		}
		return "[" + strings.Join(a, ", ") + "]"
	case map[string]any:
		a := make([]string, 0, len(x))
		for _, k := range keys(x) {
			a = append(a, k+" = "+scalar(x[k]))
		}
		return strings.Join(a, ", ")
	default:
		return fmt.Sprint(x)
	}
}
func keys(m map[string]any) []string {
	k := make([]string, 0, len(m))
	for s := range m {
		k = append(k, s)
	}
	sort.Strings(k)
	return k
}
func field(m map[string]any, key string) string {
	v, ok := m[key]
	if !ok || v == nil {
		return "기록 없음"
	}
	return scalar(v)
}
func formatTests(v any) string {
	tests, ok := displayValue(v).([]any)
	if !ok {
		return "기록 없음"
	}
	if len(tests) == 0 {
		return "없음"
	}
	out := make([]string, 0, len(tests))
	for i, raw := range tests {
		m, ok := raw.(map[string]any)
		if !ok {
			out = append(out, fmt.Sprintf("%d. %s", i+1, scalar(raw)))
			continue
		}
		out = append(out, fmt.Sprintf("%d. 입력: %s / 기대 결과: %s", i+1, field(m, "inputs"), field(m, "want")))
	}
	return strings.Join(out, "\r\n")
}
func visibleNames(v any) string {
	m, ok := v.(map[string]any)
	if !ok {
		return "기록 없음"
	}
	var names []string
	for _, name := range keys(m) {
		if present, ok := m[name].(bool); ok && present {
			names = append(names, name)
		}
	}
	if len(names) == 0 {
		return "없음"
	}
	return strings.Join(names, ", ")
}
func formatDecisionContext(v map[string]any) string {
	m := displayMap(v)
	if m == nil {
		return "판단 문맥: 기록 없음"
	}
	var out []string
	out = append(out, "요구사항: "+field(m, "intent"))
	if inputs, ok := m["inputs"].([]any); ok {
		hints, _ := m["type_hints"].(map[string]any)
		for _, input := range inputs {
			name, ok := input.(string)
			if ok {
				out = append(out, "입력 "+name+": "+field(hints, name))
			}
		}
	} else {
		out = append(out, "입력: 기록 없음")
	}
	out = append(out, "반환 타입: "+field(m, "output_type_hint"), "허용 타입: "+field(m, "allowed_types"))
	if lits, ok := m["literals"].(map[string]any); ok {
		out = append(out, "정수 상수: "+field(lits, "integers"), "문자열 상수: "+field(lits, "strings"))
	} else {
		out = append(out, "허용 상수: 기록 없음")
	}
	out = append(out, "공개 테스트:", formatTests(m["public_tests"]))
	if parents, ok := m["parents"].(map[string]any); ok {
		out = append(out, "현재 경로:")
		for _, id := range keys(parents) {
			p, _ := parents[id].(map[string]any)
			out = append(out, "경로 ID: "+id, "현재 코드: "+field(p, "prefix"), "사용 가능한 이름: "+visibleNames(p["visible_symbols"]))
		}
	} else {
		out = append(out, "현재 경로: 기록 없음")
	}
	out = append(out, "선택 지시문: "+field(m, "instructions"))
	if criteria, ok := m["criteria"].(map[string]any); ok {
		out = append(out, "모델 요청에 포함된 후보 (전송 상태는 상단 참고):")
		if len(criteria) == 0 {
			out = append(out, "없음")
		}
		for _, id := range keys(criteria) {
			c, ok := criteria[id].(map[string]any)
			if !ok {
				out = append(out, "후보 "+id+": "+scalar(criteria[id]))
				continue
			}
			out = append(out, fmt.Sprintf("후보 ID: %s / 상위 경로: %s / 토큰: %s / 종류: %s / 규칙: %s", id, field(c, "parent_id"), field(c, "lexeme"), field(c, "token_kind"), field(c, "rule")))
		}
	} else {
		out = append(out, "모델에 전달한 후보: 기록 없음")
	}
	return strings.Join(out, "\r\n")
}
func formatCandidate(c application.CandidateItem, ctx map[string]any) string {
	sent := "전달 여부: 기록 없음"
	if criteria, ok := ctx["criteria"].(map[string]any); ok {
		if _, found := criteria[c.ID]; found {
			sent = "모델에 전달됨"
		} else {
			sent = "모델에 전달되지 않음"
		}
	}
	rank := "기록 없음"
	if c.Rank > 0 {
		rank = strconv.Itoa(c.Rank)
	}
	score := "기록 없음"
	if c.Score != nil {
		score = strconv.FormatFloat(*c.Score, 'f', -1, 64)
	}
	reasons := map[string]string{"available": "평가 대기", "ranked": "순위 산정", "active": "선택", "pruned": "제외", "pruned_strict": "제외", "duplicate": "중복 제외", "forced": "강제 선택", "public_passed": "공개 테스트 통과", "public_failed": "공개 테스트 실패", "body_rejected": "본문 검사 제외", "declaration_rejected": "선언 검사 제외"}
	reason := reasons[c.Reason]
	if reason == "" {
		reason = "기록 없음"
	}
	if c.Reason != "" {
		reason += " (" + c.Reason + ")"
	}
	if c.RejectionReason != "" {
		reason += " · " + rejectionDetail(c) + " (" + c.RejectionReason + ")"
	}
	return fmt.Sprintf("후보 ID: %s / 상위 경로: %s\r\n토큰: %s / 종류: %s / 순위: %s / 점수: %s\r\n%s / 상태: %s\r\n코드: %s", scalar(c.ID), scalar(c.ParentID), scalar(c.Lexeme), scalar(c.TokenKind), rank, score, sent, reason, scalar(c.Code))
}

func rejectionDetail(c application.CandidateItem) string {
	label := RejectionLabel(c.RejectionReason)
	if c.RejectionTokenIndex != nil && *c.RejectionTokenIndex >= 0 {
		label += fmt.Sprintf(" (%d번째 토큰)", *c.RejectionTokenIndex+1)
	}
	return label
}
func formatProblem(p *application.PublicProblem) string {
	if p == nil {
		return "문제 정보를 사용할 수 없습니다."
	}
	return fmt.Sprintf("문제: %s\r\n이름: %s\r\n요구사항: %s\r\n공개 테스트:\r\n%s", scalar(p.ID), scalar(p.Label), scalar(p.Intent), formatTests(p.PublicTests))
}

var resultLabels = map[string]string{"status": "상태", "source": "생성 코드", "public_passed": "공개 테스트 통과", "heldout_passed": "보류 테스트 통과", "calls": "모델 호출", "candidates": "후보 수", "engine": "엔진", "strategy": "전략", "latency_ms": "소요 시간(ms)", "problem_id": "문제 ID", "result": "결과"}

func formatTree(v any, indent string) []string {
	switch x := v.(type) {
	case map[string]any:
		var out []string
		for _, k := range keys(x) {
			label := resultLabels[k]
			if label == "" {
				label = k
			}
			switch x[k].(type) {
			case map[string]any, []any:
				out = append(out, indent+label+":")
				out = append(out, formatTree(x[k], indent+"  ")...)
			default:
				out = append(out, indent+label+": "+scalar(x[k]))
			}
		}
		return out
	case []any:
		var out []string
		for i, item := range x {
			switch item.(type) {
			case map[string]any, []any:
				out = append(out, fmt.Sprintf("%s%d.", indent, i+1))
				out = append(out, formatTree(item, indent+"  ")...)
			default:
				out = append(out, indent+scalar(item))
			}
		}
		if len(out) == 0 {
			out = append(out, indent+"없음")
		}
		return out
	default:
		return []string{indent + scalar(v)}
	}
}
func formatResult(s application.Snapshot) string {
	if s.JobKind == "hypothesis" && len(s.Payload) > 0 {
		var report struct {
			Summary string `json:"summary"`
		}
		if json.Unmarshal(s.Payload, &report) == nil && report.Summary != "" {
			return report.Summary
		}
	}
	var v any
	if len(s.Payload) > 0 && string(s.Payload) != "null" {
		if decodeDisplayJSON(s.Payload, &v) != nil {
			return "결과를 표시할 수 없습니다."
		}
	} else if len(s.Results) > 0 {
		v = displayValue(s.Results)
	} else {
		return "아직 결과 없음"
	}
	if v == nil {
		return "아직 결과 없음"
	}
	return strings.Join(formatTree(v, ""), "\r\n")
}
