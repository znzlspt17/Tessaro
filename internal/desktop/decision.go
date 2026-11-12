package desktop

import (
	"encoding/json"
	"fmt"
	"go/scanner"
	"go/token"
	"jevcancode/internal/application"
	"sort"
	"strings"
	"unicode/utf16"
)

// 2026-11-08: 후보 ID와 부모를 함께 보존하고 기록된 요청에 포함된 항목만 분류한다.
func decisionRows(s application.Snapshot, chosen, parent, query string) (sent, excluded []application.CandidateItem) {
	criteria, known := s.DecisionContext["criteria"].(map[string]any)
	for _, c := range s.Candidates {
		if parent != "" && c.ParentID != parent {
			continue
		}
		if query != "" && !strings.Contains(strings.ToLower(c.ID+" "+c.ParentID+" "+c.Lexeme+" "+c.TokenKind+" "+c.RejectionReason), strings.ToLower(query)) {
			continue
		}
		_, inRequest := criteria[c.ID]
		if known && inRequest {
			sent = append(sent, c)
		} else if c.Reason == "body_rejected" || c.Reason == "declaration_rejected" {
			excluded = append(excluded, c)
		}
	}
	sort.SliceStable(sent, func(i, j int) bool {
		if (sent[i].ID == chosen) != (sent[j].ID == chosen) {
			return sent[i].ID == chosen
		}
		if sent[i].Rank > 0 && sent[j].Rank > 0 {
			return sent[i].Rank < sent[j].Rank
		}
		if sent[i].Rank > 0 {
			return true
		}
		if sent[j].Rank > 0 {
			return false
		}
		return sent[i].ID < sent[j].ID
	})
	return
}

// 2026-11-08: 기존 바이트는 그대로 두고 Go 스캐너가 판별한 구분자 뒤에만 표시용 줄바꿈을 추가한다.
func previewCode(prefix, lexeme string) (string, int, int) {
	source := prefix
	if lexeme != "" {
		if source != "" {
			source += " "
		}
		source += lexeme
	}
	tokenStart := len(source) - len(lexeme)
	fs := token.NewFileSet()
	f := fs.AddFile("preview.go", fs.Base(), len(source))
	var sc scanner.Scanner
	sc.Init(f, []byte(source), nil, scanner.ScanComments)
	breaks := map[int]bool{}
	for {
		pos, tok, lit := sc.Scan()
		if tok == token.EOF {
			break
		}
		off := f.Offset(pos)
		if (tok == token.SEMICOLON && lit != "\n") || tok == token.LBRACE || tok == token.RBRACE {
			if off < len(source) {
				breaks[off] = true
			}
		}
	}
	var b strings.Builder
	start, end := 0, 0
	for i, r := range source {
		if i == tokenStart {
			start = len(utf16.Encode([]rune(b.String())))
		}
		b.WriteRune(r)
		if breaks[i] && i < tokenStart {
			b.WriteString("\r\n    ")
		}
	}
	if lexeme == "" {
		start = len(utf16.Encode([]rune(b.String())))
	}
	end = start + len(utf16.Encode([]rune(lexeme)))
	return b.String(), start, end
}
func decisionSummary(s application.Snapshot, chosen, status string) string {
	var selected string
	if status == "forced" && s.ForcedLexeme != "" {
		selected = "모델 호출 없이 확정: " + s.ForcedLexeme
	}
	for _, c := range s.Candidates {
		if c.ID == chosen && chosen != "" {
			name := "모델"
			if strings.Contains(strings.ToLower(s.Model), "jev") {
				name = "Jev"
			}
			selected = fmt.Sprintf("%s가 선택: %s\r\n부모 경로: %s", name, c.Lexeme, c.ParentID)
			if c.Score != nil {
				selected += fmt.Sprintf("\r\n모델 점수: %.4f", *c.Score)
			}
			break
		}
	}
	if selected == "" {
		switch status {
		case "prepared":
			selected = "요청 준비 중"
		case "calling":
			selected = "모델 응답 대기 중"
		case "failed":
			selected = "모델 요청 실패"
		default:
			selected = "Jev 실제 선택: 기록 없음"
			for _, c := range s.Candidates {
				if c.Rank == 1 {
					selected += "\r\n점수상 1위: " + c.Lexeme
					break
				}
			}
		}
	}
	var kept []string
	for _, c := range s.Candidates {
		if c.Reason == "active" || c.Reason == "public_passed" {
			kept = append(kept, c.Lexeme+" ("+c.ID+")")
		}
		if c.Reason == "forced" {
			selected = "모델 호출 없이 확정: " + c.Lexeme
		}
	}
	if len(kept) == 0 {
		for _, p := range s.Frontier {
			if p.ID != "" {
				kept = append(kept, p.ID)
			}
		}
	}
	if len(kept) == 0 {
		selected += "\r\n\r\n탐색기가 유지: 기록 없음"
	} else {
		selected += "\r\n\r\n탐색기가 유지: " + strings.Join(kept, ", ")
	}
	return selected
}

func snapshotDecision(text string) (string, string) {
	var v struct {
		ChosenID string `json:"chosen_id"`
		Status   string `json:"decision_status"`
	}
	if json.Unmarshal([]byte(text), &v) != nil {
		return "", ""
	}
	return v.ChosenID, v.Status
}
func decisionTimeline(frames []Frame) []Frame {
	var out []Frame
	for _, f := range frames {
		if f.Group == "" {
			continue
		}
		if len(out) > 0 && out[len(out)-1].Group == f.Group {
			out[len(out)-1] = f
		} else {
			out = append(out, f)
		}
	}
	return out
}
func formatCompactContext(v map[string]any) string {
	m := displayMap(v)
	if m == nil {
		return "판단 문맥: 기록 없음"
	}
	var out []string
	out = append(out, "요구사항\r\n"+field(m, "intent"))
	if inputs, ok := m["inputs"].([]any); ok {
		hints, _ := m["type_hints"].(map[string]any)
		for _, value := range inputs {
			if name, ok := value.(string); ok {
				out = append(out, "입력 "+name+": "+field(hints, name))
			}
		}
	}
	out = append(out, "반환 타입: "+field(m, "output_type_hint"), "허용 타입: "+field(m, "allowed_types"))
	if literals, ok := m["literals"].(map[string]any); ok {
		out = append(out, "정수 상수: "+field(literals, "integers"), "문자열 상수: "+field(literals, "strings"))
	}
	out = append(out, "공개 테스트\r\n"+formatTests(m["public_tests"]))
	if parents, ok := m["parents"].(map[string]any); ok {
		for _, id := range keys(parents) {
			p, _ := parents[id].(map[string]any)
			out = append(out, "경로 "+id+" · 사용 가능한 이름: "+visibleNames(p["visible_symbols"]))
		}
	}
	return strings.Join(out, "\r\n\r\n")
}
func recordedCode(s application.Snapshot, id string) string {
	for _, p := range s.Frontier {
		if p.ID == id && p.Code != "" {
			return p.Code
		}
	}
	for _, c := range s.Candidates {
		if c.ID == id && c.Code != "" {
			return c.Code
		}
	}
	return ""
}
func displayProblem(s application.Snapshot) *application.PublicProblem {
	if s.Current == nil && s.DecisionContext == nil {
		return nil
	}
	p := application.PublicProblem{}
	if s.Current != nil {
		p = *s.Current
	}
	if p.Intent == "" {
		p.Intent, _ = s.DecisionContext["intent"].(string)
	}
	if p.PublicTests == nil {
		p.PublicTests = s.DecisionContext["public_tests"]
	}
	return &p
}
func keptCode(s application.Snapshot, parent, selected string) string {
	var first, rest []string
	for _, path := range s.Frontier {
		if (path.ParentID == parent || path.ID == parent) && path.Code != "" {
			value := "[유지 " + path.ID + "]\r\n" + path.Code
			if path.ID == selected {
				first = append(first, value)
			} else {
				rest = append(rest, value)
			}
		}
	}
	return strings.Join(append(first, rest...), "\r\n\r\n")
}
func decisionHeadline(s application.Snapshot, chosen, status string) string {
	if status == "forced" && s.ForcedLexeme != "" {
		return s.ForcedLexeme
	}
	for _, c := range s.Candidates {
		if chosen != "" && c.ID == chosen {
			return c.Lexeme
		}
	}
	return "—"
}
