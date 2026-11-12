package tokensynth

import (
	"context"
	"encoding/json"
	"jevcancode/internal/grammar"
	"jevcancode/internal/rank"
	"jevcancode/internal/tokenspec"
	"os"
	"reflect"
	"strings"
	"testing"
)

func clampSpec(t *testing.T) tokenspec.Problem {
	t.Helper()
	f, err := os.Open("../../examples/token/specs/conditional-clamp.json")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	p, err := tokenspec.Decode(f)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func TestInvalidPriorOrderingReportsOriginalIndex(t *testing.T) {
	p := clampSpec(t)
	tokens := strings.Fields("package main ; func f0 ( x int ) int { if x < 2 < 2")
	s := advanceContract(t, p, tokens)
	called := false
	var events []Event
	result, _ := searchFrom(context.Background(), p, candidateRecorder{fn: func([]rank.Candidate) { called = true }}, Options{Strategy: "beam", Trace: func(e Event) { events = append(events, e) }}, func(string) (bool, error) { return false, nil }, []*State{s})
	if called || result.Calls != 0 {
		t.Fatalf("rank called for invalid state: %+v", result)
	}
	for _, e := range events {
		if e.Reason == "body_rejected" && e.RejectionReason == "bool_order_comparison" {
			if e.RejectionTokenIndex == nil || *e.RejectionTokenIndex != len(tokens)-2 {
				t.Fatalf("wrong index: %+v", e)
			}
			return
		}
	}
	t.Fatalf("missing rejection: %+v", events)
}

func TestClampElseIfAndNegativeOrderingCandidates(t *testing.T) {
	for _, tc := range []struct{ body, op string }{
		{"if x > 2 { return 2 ; } else if x < 2", "<"}, {"if x >= - 2", "<="},
	} {
		t.Run(tc.body, func(t *testing.T) {
			p := clampSpec(t)
			s := advanceContract(t, p, strings.Fields("package main ; func f0 ( x int ) int { "+tc.body))
			var offered []rank.Candidate
			found := false
			_, _ = searchFrom(context.Background(), p, candidateRecorder{fn: func(cs []rank.Candidate) { offered = cs }}, Options{Strategy: "beam", Trace: func(e Event) {
				if e.Reason == "body_rejected" && e.RejectionReason == "bool_order_comparison" && e.Lexeme == tc.op {
					found = true
				}
			}}, func(string) (bool, error) { return false, nil }, []*State{s})
			if !found {
				t.Fatalf("missing rejected %s", tc.op)
			}
			for _, c := range offered {
				if c.Lexeme == tc.op {
					t.Fatalf("offered %s", tc.op)
				}
			}
		})
	}
}

func TestCompletedOriginsQueryIsReadOnlyAndIncludesNestedSuffix(t *testing.T) {
	g, err := Grammar()
	if err != nil {
		t.Fatal(err)
	}
	p := g.NewState("source_file")
	tokens := strings.Fields("package main ; func f0 ( x int ) int { if x < 2")
	for _, v := range tokens {
		p, err = p.Advance(v)
		if err != nil {
			t.Fatal(err)
		}
	}
	before, accepts := p.Next(), p.Accepts()
	origins := p.CompletedOrigins(len(tokens), "_expression")
	if len(origins) < 2 || origins[0] != len(tokens)-3 || origins[len(origins)-1] != len(tokens)-1 {
		t.Fatalf("unexpected expression origins: %v", origins)
	}
	saved := append([]int(nil), origins...)
	origins[0] = -1
	if !reflect.DeepEqual(saved, p.CompletedOrigins(len(tokens), "_expression")) || !reflect.DeepEqual(before, p.Next()) || accepts != p.Accepts() {
		t.Fatal("read-only query modified chart")
	}
}

func TestOnlyOrderingCandidateIsNotForced(t *testing.T) {
	p := clampSpec(t)
	tokens := strings.Fields("package main ; func f0 ( x int ) int { if x < 2")
	s := advanceContract(t, p, tokens)
	str := func(v string) any { return map[string]any{"type": "STRING", "value": v} }
	sym := func(v string) any { return map[string]any{"type": "SYMBOL", "name": v} }
	var prefix []any
	for _, v := range tokens[:len(tokens)-3] {
		prefix = append(prefix, str(v))
	}
	prefix = append(prefix, sym("_expression"))
	doc := map[string]any{"name": "forced_comparison", "rules": map[string]any{
		"source_file":       map[string]any{"type": "SEQ", "members": prefix},
		"_expression":       map[string]any{"type": "CHOICE", "members": []any{sym("binary_expression"), str("x"), str("2")}},
		"binary_expression": map[string]any{"type": "SEQ", "members": []any{sym("_expression"), str("<"), sym("_expression")}},
	}}
	data, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	g, err := grammar.Load(data)
	if err != nil {
		t.Fatal(err)
	}
	ps := g.NewState("source_file")
	for _, v := range tokens {
		ps, err = ps.Advance(v)
		if err != nil {
			t.Fatal(err)
		}
	}
	s.Parser = ps
	choices, _, err := Candidates(p, s)
	if err != nil {
		t.Fatal(err)
	}
	if len(choices) != 1 || choices[0].Lexeme != "<" {
		t.Fatalf("fixture candidates=%+v", choices)
	}
	called := false
	var events []Event
	result, _ := searchFrom(context.Background(), p, candidateRecorder{fn: func([]rank.Candidate) { called = true }}, Options{Strategy: "strict-greedy", Trace: func(e Event) { events = append(events, e) }}, func(string) (bool, error) { return false, nil }, []*State{s})
	if called || result.Calls != 0 || result.ForcedSteps != 0 || result.Candidates != 1 {
		t.Fatalf("called=%v result=%+v", called, result)
	}
	for _, e := range events {
		if e.Reason == "body_rejected" && e.RejectionReason == "bool_order_comparison" {
			if e.ID != "00000001" || e.RejectionTokenIndex == nil || *e.RejectionTokenIndex != len(tokens) {
				t.Fatalf("wrong event=%+v", e)
			}
			return
		}
	}
	t.Fatalf("missing rejection: %+v", events)
}

func TestOrderingAfterComparisonIsExcludedBeforeModel(t *testing.T) {
	p := clampSpec(t)
	s := advanceContract(t, p, strings.Fields("package main ; func f0 ( x int ) int { if x < 2"))
	var offered []rank.Candidate
	var events []Event
	result, _ := searchFrom(context.Background(), p, candidateRecorder{fn: func(cs []rank.Candidate) { offered = cs }}, Options{Strategy: "beam", Trace: func(e Event) { events = append(events, e) }}, func(string) (bool, error) { return false, nil }, []*State{s})
	if len(offered) == 0 {
		t.Fatal("ranker did not receive candidates")
	}
	offeredValues := map[string]bool{}
	for _, c := range offered {
		offeredValues[c.Lexeme] = true
		switch c.Lexeme {
		case "<", "<=", ">", ">=":
			t.Fatalf("ordered bool candidate %s reached ranker", c.Lexeme)
		}
	}
	for _, lexeme := range []string{"+", "==", "&&", "||"} {
		if !offeredValues[lexeme] {
			t.Errorf("valid continuation %q not offered", lexeme)
		}
	}
	count := 0
	for _, e := range events {
		if e.Reason == "body_rejected" && e.RejectionReason == "bool_order_comparison" {
			count++
			if e.RejectionTokenIndex == nil || *e.RejectionTokenIndex != len(s.Tokens) || e.Candidates <= 0 {
				t.Fatalf("wrong event: %+v", e)
			}
		}
	}
	if count == 0 || result.Candidates < count {
		t.Fatalf("rejections=%d candidate count=%d", count, result.Candidates)
	}
}

func TestComparisonBindingContexts(t *testing.T) {
	for _, tc := range []struct {
		body   string
		reject bool
	}{
		{"if x < 2", true}, {"if x < 2 + 1", true}, {"if ( x < 2 )", true},
		{"if x < 2 && x", false}, {"if x < 2 && x < 2", true}, {"if x < 2 || x < 2", true},
		{"if f0 ( x < 2 )", false}, {"if f0 ( x < 2", true}, {"if xs [ x < 2", true},
		{"if x < 2 +", false}, {"if x < 2 == true", true}, {"if x", false},
		{"if x > 2 { return 2 ; } else if x < 2", true}, {"if x >= - 2", true},
		{"if xs [ x < 2 ]", false}, {"if f0 ( x , x < 2", true}, {"if f0 ( x < 2 , x", false},
	} {
		t.Run(tc.body, func(t *testing.T) {
			g, err := Grammar()
			if err != nil {
				t.Fatal(err)
			}
			p := g.NewState("source_file")
			tokens := strings.Fields("package main ; func f0 ( x int ) int { " + tc.body)
			for _, tok := range tokens {
				p, err = p.Advance(tok)
				if err != nil {
					t.Fatalf("advance %q: %v", tok, err)
				}
			}
			s := &State{Tokens: tokens, Parser: p}
			if got := s.comparisonResultBefore(len(tokens)); got != tc.reject {
				t.Fatalf("reject=%v want=%v origins=%v", got, tc.reject, p.CompletedOrigins(len(tokens), "_expression"))
			}
		})
	}
}

func TestValidComparisonProgramsReachCompletion(t *testing.T) {
	for _, body := range []string{
		"if a < b + a { return a ; } ; return b ;",
		"if a < b && b < a { return a ; } ; return b ;",
		"if ( a < b ) == true { return a ; } ; return b ;",
		"if ( a < b ) == ( b < a ) { return a ; } ; return b ;",
	} {
		t.Run(body, func(t *testing.T) {
			target := "package main ; func f0 ( a int , b int ) int { " + body + " }"
			out, err := Search(context.Background(), subtractionSpec(t), scripted(target), Options{Strategy: "strict-greedy"}, func(s string) (bool, error) { return s == target, nil })
			if err != nil || out.Source != target {
				t.Fatalf("valid program blocked: %v %+v", err, out)
			}
		})
	}
}
