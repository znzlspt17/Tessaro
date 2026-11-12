package tokensynth

import (
	"context"
	"encoding/json"
	"tessaro/internal/grammar"
	"tessaro/internal/rank"
	"reflect"
	"strings"
	"testing"
)

func TestStandaloneBinaryCandidateRejectedBeforeRank(t *testing.T) {
	p := subtractionSpec(t)
	s := advanceContract(t, p, strings.Fields("package main ; func f0 ( a int , b int ) int { a"))
	var offered []rank.Candidate
	var rejected bool
	_, _ = searchFrom(context.Background(), p, candidateRecorder{fn: func(cs []rank.Candidate) { offered = append(offered, cs...) }}, Options{Strategy: "beam", Trace: func(e Event) {
		if e.Reason == "body_rejected" && e.Lexeme == "+" && e.RejectionReason == "irrecoverable_expression_statement" {
			rejected = true
		}
	}}, func(string) (bool, error) { return false, nil }, []*State{s})
	for _, c := range offered {
		if c.Lexeme == "+" {
			t.Fatal("standalone binary operator offered to ranker")
		}
	}
	if !rejected {
		t.Fatal("standalone binary operator not rejected")
	}
}

// 이 작은 문법은 실제 탐색 루프의 단일 후보 경계를 고정하는 배선 테스트다.
// 원본 Go 문법은 TestStandaloneBinaryCandidateRejectedBeforeRank에서 별도로 검증한다.
func TestOnlyCandidateRejectedBeforeForcedSelectionAndCounted(t *testing.T) {
	p := subtractionSpec(t)
	tokens := strings.Fields("package main ; func f0 ( a int , b int ) int { a")
	s := advanceContract(t, p, tokens)
	members := make([]any, 0, len(tokens))
	for _, tok := range tokens[:len(tokens)-1] {
		members = append(members, map[string]any{"type": "STRING", "value": tok})
	}
	members = append(members, map[string]any{"type": "SYMBOL", "name": "expression_statement"})
	doc := map[string]any{"name": "forced_fixture", "rules": map[string]any{
		"source_file":          map[string]any{"type": "SEQ", "members": members},
		"expression_statement": map[string]any{"type": "SYMBOL", "name": "binary_expression"},
		"binary_expression": map[string]any{"type": "PREC_LEFT", "content": map[string]any{"type": "SEQ", "members": []any{
			map[string]any{"type": "STRING", "value": "a"}, map[string]any{"type": "STRING", "value": "+"}, map[string]any{"type": "STRING", "value": "b"},
		}}},
	}}
	data, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	g, err := grammar.Load(data)
	if err != nil {
		t.Fatal(err)
	}
	parser := g.NewState("source_file")
	for _, tok := range tokens {
		parser, err = parser.Advance(tok)
		if err != nil {
			t.Fatal(err)
		}
	}
	s.Parser = parser
	choices, _, err := Candidates(p, s)
	if err != nil {
		t.Fatal(err)
	}
	if len(choices) != 1 || choices[0].Lexeme != "+" {
		t.Fatalf("fixture choices: %+v", choices)
	}
	var events []Event
	called := false
	result, _ := searchFrom(context.Background(), p, candidateRecorder{fn: func([]rank.Candidate) { called = true }}, Options{Strategy: "strict-greedy", Trace: func(e Event) { events = append(events, e) }}, func(string) (bool, error) { return false, nil }, []*State{s})
	if called || result.Calls != 0 || result.Candidates != 1 || result.ForcedSteps != 0 {
		t.Fatalf("called=%v result=%+v", called, result)
	}
	var rejectionCount int
	for _, e := range events {
		if e.Reason == "forced" {
			t.Fatalf("invalid candidate forced: %+v", e)
		}
		if e.Reason == "body_rejected" && e.RejectionReason == "irrecoverable_expression_statement" {
			rejectionCount++
			if e.ID != "00000001" || e.ParentID != s.ID || e.Lexeme != "+" || e.Candidates != 1 {
				t.Fatalf("wrong rejection accounting: %+v", e)
			}
		}
	}
	if rejectionCount != 1 {
		t.Fatalf("got %d rejection events: %+v", rejectionCount, events)
	}
}

func TestIrrecoverableInitialStateNeverCallsRanker(t *testing.T) {
	p := subtractionSpec(t)
	s := advanceContract(t, p, strings.Fields("package main ; func f0 ( a int , b int ) int { a + b + b + a - b + 0 +"))
	called, rejected := false, false
	result, _ := searchFrom(context.Background(), p, candidateRecorder{fn: func([]rank.Candidate) { called = true }}, Options{Strategy: "beam", Trace: func(e Event) {
		if e.Reason == "body_rejected" && e.RejectionReason == "irrecoverable_expression_statement" {
			rejected = true
		}
	}}, func(string) (bool, error) { return false, nil }, []*State{s})
	if called || !rejected || result.Calls != 0 || result.Candidates != 0 {
		t.Fatalf("called=%v rejected=%v result=%+v", called, rejected, result)
	}
}

func TestBinaryContextQueryDoesNotModifyParser(t *testing.T) {
	g, err := Grammar()
	if err != nil {
		t.Fatal(err)
	}
	s := g.NewState("source_file")
	for _, lexeme := range strings.Fields("package main ; func f0 ( a int ) int { a +") {
		s, err = s.Advance(lexeme)
		if err != nil {
			t.Fatal(err)
		}
	}
	before, accepts := s.Next(), s.Accepts()
	if !s.BinaryOnlyStatementLeft(len(strings.Fields("package main ; func f0 ( a int ) int { a +")), "+") {
		t.Fatal("missing binary context")
	}
	if !reflect.DeepEqual(before, s.Next()) || accepts != s.Accepts() {
		t.Fatal("context query modified parser")
	}
}

func TestValidBinaryContextsCompleteThroughSearch(t *testing.T) {
	for _, body := range []string{"return a + b ;", "a = a + b ; return a ;", "if a + b > a { return a ; } ; return b ;"} {
		t.Run(body, func(t *testing.T) {
			target := "package main ; func f0 ( a int , b int ) int { " + body + " }"
			out, err := Search(context.Background(), subtractionSpec(t), scripted(target), Options{Strategy: "strict-greedy"}, func(s string) (bool, error) { return s == target, nil })
			if err != nil || out.Source != target {
				t.Fatalf("failed valid path: %v, %+v", err, out)
			}
		})
	}
}

func TestBinaryStatementContexts(t *testing.T) {
	for _, tc := range []struct {
		body   string
		reject bool
	}{
		{"a +", true}, {"0 +", true}, {"* a +", false}, {"f0 ( a , b ) +", true}, {"xs [ a ] +", true},
		{"v0 = a ; a +", true}, {"{ a +", true}, {"if a > b { a +", false},
		{"return a +", false}, {"a = a +", false}, {"if a +", false}, {"for a +", false},
		{"xs [ a +", false}, {"f0 ( a +", false}, {"( a +", false},
		{"if a = b ; a +", false}, {"for a = b ; a +", false},
	} {
		t.Run(tc.body, func(t *testing.T) {
			g, err := Grammar()
			if err != nil {
				t.Fatal(err)
			}
			parser := g.NewState("source_file")
			prefix := "package main ; func f0 ( a int , b int ) int { " + tc.body
			var before []string
			for _, lexeme := range strings.Fields(prefix) {
				parser, err = parser.Advance(lexeme)
				if err != nil {
					t.Fatalf("%s: %v", lexeme, err)
				}
				before = append(before, lexeme)
			}
			state := &State{Tokens: before, Parser: parser}
			if got := state.irrecoverableExpression(""); got != tc.reject {
				t.Fatalf("reject = %v, want %v", got, tc.reject)
			}
		})
	}
}
