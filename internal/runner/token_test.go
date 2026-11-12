package runner

import (
	"context"
	"encoding/json"
	"fmt"
	"jevcancode/internal/rank"
	"jevcancode/internal/tokenspec"
	"jevcancode/internal/tokensynth"
	"strings"
	"testing"
)

func TestTokenBudgetDoesNotReadHeldout(t *testing.T) {
	p, err := tokenspec.Decode(strings.NewReader(`{"intent":"identity","inputs":["x"],"allowed_types":["int"],"public_tests":[{"inputs":{"x":1},"want":1}]}`))
	if err != nil {
		t.Fatal(err)
	}
	called := false
	out := RunToken(context.Background(), p, rank.Fake{}, Request{Strategy: "beam", MaxCandidates: 1, TokenHeldout: func() ([]tokenspec.Test, error) { called = true; return nil, nil }})
	if called || out.Status != "budget_exhausted" || out.Engine != "token" || out.CandidateUnit != "go_lexical_token" || out.GrammarRevision == "" {
		t.Fatalf("heldout=%v %+v", called, out)
	}
}

type scriptedTokenRanker struct {
	target      string
	calls       int
	privateSeen bool
}

func (r *scriptedTokenRanker) Rank(ctx context.Context, state string, cs []rank.Candidate) ([]rank.Score, error) {
	scores, _, err := r.RankWithUsage(ctx, state, cs)
	return scores, err
}
func (r *scriptedTokenRanker) RankWithUsage(_ context.Context, state string, cs []rank.Candidate) ([]rank.Score, *rank.Usage, error) {
	r.calls++
	if strings.Contains(state, "1007") {
		r.privateSeen = true
	}
	var parsed struct {
		Parents map[string]struct {
			Prefix string `json:"prefix"`
		} `json:"parents"`
	}
	if err := json.Unmarshal([]byte(state), &parsed); err != nil {
		return nil, nil, err
	}
	out := make([]rank.Score, len(cs))
	for i, c := range cs {
		prefix := strings.TrimSpace(parsed.Parents[c.ParentID].Prefix + " " + c.Lexeme)
		score := 0.0
		if strings.HasPrefix(r.target, prefix) {
			score = 1000 + float64(len(prefix))
		}
		out[i] = rank.Score{ID: c.ID, Score: score}
	}
	in, outCount := 2, 3
	return out, &rank.Usage{InputTokens: &in, OutputTokens: &outCount}, nil
}
func TestTokenPublicThenHeldoutUsageAndTrace(t *testing.T) {
	p, err := tokenspec.Decode(strings.NewReader(`{"intent":"subtract","inputs":["a","b"],"allowed_types":["int"],"public_tests":[{"inputs":{"a":3,"b":1},"want":2}]}`))
	if err != nil {
		t.Fatal(err)
	}
	r := &scriptedTokenRanker{target: "package main ; func f0 ( a int , b int ) int { return a - b ; }"}
	called := 0
	callsAtHeldout := 0
	var events []tokensynth.Event
	inputPrice, outputPrice := 1.0, 2.0
	out := RunToken(context.Background(), p, r, Request{Strategy: "strict-greedy", MaxLocals: 0, LocalLimitSet: true, InputPrice: &inputPrice, OutputPrice: &outputPrice, TokenTrace: func(e tokensynth.Event) { events = append(events, e) }, TokenHeldout: func() ([]tokenspec.Test, error) {
		called++
		callsAtHeldout = r.calls
		return []tokenspec.Test{{Inputs: map[string]any{"a": 9, "b": 4}, Want: 1007}}, nil
	}})
	if out.Status != "heldout_failed" || !out.PublicPassed || out.HeldoutPassed == nil || *out.HeldoutPassed || called != 1 || callsAtHeldout != r.calls || r.privateSeen {
		t.Fatalf("result=%+v calls=%d held=%d private=%v", out, r.calls, called, r.privateSeen)
	}
	if out.TokenCount == 0 || out.ForcedTokens+out.ModelSelectedTokens != out.TokenCount || out.Usage == nil || out.Usage.InputTokens == nil || *out.Usage.InputTokens != r.calls*2 || out.EstimatedCost == nil || out.HeldoutHash == "" {
		t.Fatalf("metrics=%+v", out)
	}
	var firstToken *tokensynth.Event
	for i := range events {
		if events[i].Lexeme != "" {
			firstToken = &events[i]
			break
		}
	}
	if firstToken == nil || firstToken.Lexeme != "package" {
		t.Fatalf("trace=%+v", events)
	}
	for _, e := range events {
		if e.Code != "" {
			t.Fatal("trace code emitted without flag")
		}
	}
}
func TestTokenCancellationAndInvalidBudget(t *testing.T) {
	p, err := tokenspec.Decode(strings.NewReader(`{"intent":"x","inputs":["x"],"allowed_types":["int"],"public_tests":[{"inputs":{"x":1},"want":1}]}`))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	out := RunToken(ctx, p, rank.Fake{}, Request{Strategy: "beam"})
	if out.Status != "cancelled" {
		t.Fatalf("%+v", out)
	}
	out = RunToken(context.Background(), p, rank.Fake{}, Request{Strategy: "beam", MaxTokens: 1000001})
	if out.Status != "input_error" {
		t.Fatalf("%+v", out)
	}
}
func TestTokenExplicitZeroLocalsControlsVerification(t *testing.T) {
	p, err := tokenspec.Decode(strings.NewReader(`{"intent":"identity","inputs":["a"],"allowed_types":["int"],"public_tests":[{"inputs":{"a":1},"want":1}]}`))
	if err != nil {
		t.Fatal(err)
	}
	target := "package main ; func f0 ( a int ) int { v0 := a ; return v0 ; }"
	r := &scriptedTokenRanker{target: target}
	zero := RunToken(context.Background(), p, r, Request{Strategy: "strict-greedy", MaxLocals: 0, LocalLimitSet: true})
	if zero.Status != "no_solution" || zero.TypecheckFailures == 0 || zero.EffectiveBudgets["max_locals"] != 0 {
		t.Fatalf("zero: %+v", zero)
	}
	r = &scriptedTokenRanker{target: target}
	one := RunToken(context.Background(), p, r, Request{Strategy: "strict-greedy", MaxLocals: 1, LocalLimitSet: true})
	if one.Status != "success" || one.UsedLocals != 1 {
		t.Fatalf("one: %+v", one)
	}
}

func TestTokenExecutionForLoopsCollectionsAndBinding(t *testing.T) {
	cases := []struct{ name, spec, source string }{
		{"reordered", `{"intent":"subtract","inputs":["a","b"],"allowed_types":["int"],"public_tests":[{"inputs":{"a":7,"b":2},"want":5}]}`, "package main ; func f0 ( b int , a int ) int { return a - b ; }"},
		{"slice_loop", `{"intent":"sum","inputs":["xs"],"allowed_types":["int","[]int"],"public_tests":[{"inputs":{"xs":[2,-1,4]},"want":5},{"inputs":{"xs":[]},"want":0}]}`, "package main ; func f0 ( xs [ ] int ) int { v0 := 0 ; for _ , v1 := range xs { v0 += v1 ; } ; return v0 ; }"},
		{"map_update", `{"intent":"increase named value","inputs":["m","k"],"allowed_types":["int","string","map[string]int"],"public_tests":[{"inputs":{"m":{"a":2},"k":"a"},"want":3},{"inputs":{"m":{},"k":"z"},"want":1}]}`, "package main ; func f0 ( k string , m map [ string ] int ) int { m [ k ] = m [ k ] + 1 ; return m [ k ] ; }"},
		{"string_index", `{"intent":"first byte","inputs":["s"],"allowed_types":["int","string"],"public_tests":[{"inputs":{"s":"a"},"want":97},{"inputs":{"s":"z"},"want":122}]}`, "package main ; func f0 ( s string ) int { return int ( s [ 0 ] ) ; }"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p, err := tokenspec.Decode(strings.NewReader(tc.spec))
			if err != nil {
				t.Fatal(err)
			}
			out := RunToken(context.Background(), p, &scriptedTokenRanker{target: tc.source}, Request{Strategy: "strict-greedy"})
			if out.Status != "success" || !out.PublicPassed || out.TokenCount == 0 || out.CompilePasses != 1 {
				t.Fatalf("%+v", out)
			}
		})
	}
}

func TestTokenLengthBudgetClassified(t *testing.T) {
	p, err := tokenspec.Decode(strings.NewReader(`{"intent":"x","inputs":["x"],"allowed_types":["int"],"public_tests":[{"inputs":{"x":1},"want":1}]}`))
	if err != nil {
		t.Fatal(err)
	}
	out := RunToken(context.Background(), p, rank.Fake{}, Request{MaxTokens: 1})
	if out.Status != "budget_exhausted" || out.Calls != 0 {
		t.Fatalf("%+v", out)
	}
}

func TestTokenTraceCodeControlsDecisionContext(t *testing.T) {
	p, err := tokenspec.Decode(strings.NewReader(`{"intent":"identity","inputs":["x"],"allowed_types":["int"],"public_tests":[{"inputs":{"x":1},"want":1}]}`))
	if err != nil {
		t.Fatal(err)
	}
	for _, enabled := range []bool{false, true} {
		t.Run(fmt.Sprint(enabled), func(t *testing.T) {
			var contexts []map[string]any
			RunToken(context.Background(), p, rank.Fake{}, Request{Strategy: "beam", MaxCalls: 1, TraceCode: enabled, TokenTrace: func(e tokensynth.Event) {
				if e.Reason == "decision_context" && e.DecisionContext != nil {
					contexts = append(contexts, e.DecisionContext)
				}
			}})
			if (len(contexts) > 0) != enabled {
				t.Fatalf("TraceCode %v: got %d contexts", enabled, len(contexts))
			}
		})
	}
	var sawContext bool
	RunToken(context.Background(), p, rank.Fake{}, Request{Strategy: "beam", MaxCalls: 1, TraceDecisionContext: true, TokenTrace: func(e tokensynth.Event) {
		if e.Code != "" {
			t.Fatal("explicit decision context enabled per-event code tracing")
		}
		if e.Reason == "decision_context" && e.DecisionContext != nil {
			sawContext = true
		}
	}})
	if !sawContext {
		t.Fatal("explicit decision context opt-in was ignored")
	}
}
