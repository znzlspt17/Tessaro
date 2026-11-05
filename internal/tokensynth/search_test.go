package tokensynth

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"jevcancoding/internal/rank"
	"jevcancoding/internal/tokenlex"
	"strings"
	"testing"
)

type scripted string

func (r scripted) Rank(_ context.Context, state string, candidates []rank.Candidate) ([]rank.Score, error) {
	var v struct {
		Parents map[string]struct {
			Prefix string `json:"prefix"`
		} `json:"parents"`
	}
	if e := json.Unmarshal([]byte(state), &v); e != nil {
		return nil, e
	}
	result := make([]rank.Score, len(candidates))
	for i, c := range candidates {
		prefix := strings.TrimSpace(v.Parents[c.ParentID].Prefix + " " + c.Lexeme)
		score := 0.
		if strings.HasPrefix(string(r), prefix) {
			score = 1000 + float64(len(prefix))
		}
		result[i] = rank.Score{ID: c.ID, Score: score}
	}
	return result, nil
}

type failedRanker struct{}

func (failedRanker) Rank(context.Context, string, []rank.Candidate) ([]rank.Score, error) {
	return nil, errors.New("test-only raw diagnostic")
}
func TestAvailableTokensRecordedWhenModelFails(t *testing.T) {
	var events []Event
	_, err := Search(context.Background(), subtractionSpec(t), failedRanker{}, Options{Strategy: "beam", Trace: func(e Event) { events = append(events, e) }}, func(string) (bool, error) { return false, nil })
	if err == nil {
		t.Fatal("expected model error")
	}
	found := false
	for _, e := range events {
		if e.Reason == "available" && e.ParentID != "" && e.Lexeme != "" {
			found = true
		}
	}
	if !found {
		t.Fatal("candidate set disappeared on request failure")
	}
	data, _ := json.Marshal(events)
	if strings.Contains(string(data), "raw diagnostic") {
		t.Fatal("raw error leaked")
	}
}

func TestFailedSearchRetainsGeneratedTokenCounts(t *testing.T) {
	result, err := Search(context.Background(), subtractionSpec(t), failedRanker{}, Options{Strategy: "beam"}, func(string) (bool, error) { return false, nil })
	if err == nil {
		t.Fatal("expected request error")
	}
	if result.GeneratedTokens == 0 || result.GeneratedTokens != result.ForcedSteps+result.ModelSteps {
		t.Fatalf("missing failed search counts: %+v", result)
	}
}

type jointRecorder struct {
	target scripted
	joint  bool
	t      *testing.T
}

func (r *jointRecorder) Rank(ctx context.Context, state string, cs []rank.Candidate) ([]rank.Score, error) {
	parents := map[string]bool{}
	for _, c := range cs {
		if c.Code != "" || c.ParentID == "" {
			r.t.Fatal("full code candidate or missing parent")
		}
		kind, err := tokenlex.Kind(c.Lexeme)
		if err != nil || kind.String() != c.TokenKind {
			r.t.Fatalf("not one lexical token: %+v", c)
		}
		parents[c.ParentID] = true
	}
	if len(parents) > 1 {
		r.joint = true
	}
	return r.target.Rank(ctx, state, cs)
}
func TestBeamJointRankingAndGlobalWidth(t *testing.T) {
	p := subtractionSpec(t)
	target := "package main ; func f0 ( a int , b int ) int { return a - b ; }"
	r := &jointRecorder{target: scripted(target), t: t}
	active := map[int]int{}
	_, err := Search(context.Background(), p, r, Options{Strategy: "beam", Trace: func(e Event) {
		if e.Reason == "active" {
			active[e.Step]++
		}
	}}, func(source string) (bool, error) { return source == target, nil })
	if err != nil || !r.joint {
		t.Fatalf("joint=%v err=%v", r.joint, err)
	}
	for step, width := range active {
		if width > 3 {
			t.Fatalf("step %d kept %d paths", step, width)
		}
	}
}
func TestFullFunctionStartsEmptyAndUsesSingleTokenCandidates(t *testing.T) {
	p := subtractionSpec(t)
	target := "package main ; func f0 ( a int , b int ) int { return a - b ; }"
	var events []Event
	r, e := Search(context.Background(), p, scripted(target), Options{Strategy: "strict-greedy", Trace: func(e Event) { events = append(events, e) }}, func(source string) (bool, error) { return source == target, nil })
	if e != nil || r.Source != target || r.ForcedTokens == 0 || r.Calls == 0 {
		t.Fatalf("not full token synthesis: %+v %v", r, e)
	}
	var firstToken *Event
	for i := range events {
		if events[i].Lexeme != "" {
			firstToken = &events[i]
			break
		}
	}
	if firstToken == nil || firstToken.Lexeme != "package" || firstToken.ParentID != "root" {
		t.Fatalf("source was prefilled: %+v", events)
	}
}

type declarationObserver struct {
	target string
	bad    bool
}

func (r *declarationObserver) Rank(_ context.Context, state string, cs []rank.Candidate) ([]rank.Score, error) {
	var v struct {
		Parents map[string]struct {
			Prefix string `json:"prefix"`
		} `json:"parents"`
	}
	if err := json.Unmarshal([]byte(state), &v); err != nil {
		return nil, err
	}
	out := make([]rank.Score, len(cs))
	for i, c := range cs {
		parent := v.Parents[c.ParentID].Prefix
		if strings.HasSuffix(parent, " )") && c.Lexeme == "{" {
			r.bad = true
		}
		candidate := strings.TrimSpace(parent + " " + c.Lexeme)
		score := 0.
		if strings.HasPrefix(r.target, candidate) {
			score = 1000 + float64(len(candidate))
		}
		out[i] = rank.Score{ID: c.ID, Score: score}
	}
	return out, nil
}
func TestDeclarationGateExcludesMissingReturnBeforeRank(t *testing.T) {
	p := subtractionSpec(t)
	target := "package main ; func f0 ( a int , b int ) int { return a - b ; }"
	r := &declarationObserver{target: target}
	rejected := false
	result, err := Search(context.Background(), p, r, Options{Strategy: "strict-greedy", Trace: func(e Event) {
		if e.Reason == "declaration_rejected" {
			if e.RejectionReason == "return_type" {
				rejected = true
			}
		}
	}}, func(s string) (bool, error) { return s == target, nil })
	if err != nil || result.Source != target || r.bad || !rejected {
		t.Fatalf("source=%q bad=%v rejected=%v err=%v", result.Source, r.bad, rejected, err)
	}
}
func replayPrefix(t *testing.T, text, id string) *State {
	t.Helper()
	s, err := NewState()
	if err != nil {
		t.Fatal(err)
	}
	for i, value := range strings.Fields(text) {
		kind, e := tokenlex.Kind(value)
		if e != nil {
			t.Fatal(e)
		}
		s, e = s.Advance(Choice{Lexeme: value, Kind: kind.String()}, fmt.Sprintf("%s-%d", id, i))
		if e != nil {
			t.Fatalf("token %q: %v", value, e)
		}
	}
	s.ID = id
	return s
}

type parentObserver struct {
	target scripted
	badID  string
	calls  int
	leaked bool
}

func (r *parentObserver) Rank(ctx context.Context, state string, cs []rank.Candidate) ([]rank.Score, error) {
	r.calls++
	var payload struct {
		Parents map[string]json.RawMessage `json:"parents"`
	}
	if err := json.Unmarshal([]byte(state), &payload); err != nil {
		return nil, err
	}
	if _, ok := payload.Parents[r.badID]; ok {
		r.leaked = true
	}
	return r.target.Rank(ctx, state, cs)
}
func TestInvalidStatePrunedWhileValidSiblingSurvives(t *testing.T) {
	p := subtractionSpec(t)
	bad := replayPrefix(t, "package main ; func f0 ( a bool , b int ) {", "bad")
	good := replayPrefix(t, "package main ; func f0 ( a int , b int ) int { return a", "good")
	target := "package main ; func f0 ( a int , b int ) int { return a - b ; }"
	r := &parentObserver{badID: "bad", target: scripted(target)}
	rejected := false
	badFrontier := false
	out, err := searchFrom(context.Background(), p, r, Options{Strategy: "beam", Trace: func(e Event) {
		if e.Reason == "declaration_rejected" && e.ID == "bad" {
			rejected = true
		}
		if e.Reason == "frontier" {
			for _, path := range e.Frontier {
				if path.ID == "bad" {
					badFrontier = true
				}
			}
		}
	}}, func(s string) (bool, error) { return s == target, nil }, []*State{bad, good})
	if err != nil || out.Source != target || r.leaked || !rejected || badFrontier {
		t.Fatalf("%+v err=%v leaked=%v rejected=%v bad_frontier=%v", out, err, r.leaked, rejected, badFrontier)
	}
}
func TestAllInvalidStatesStopBeforeRank(t *testing.T) {
	p := subtractionSpec(t)
	bad := replayPrefix(t, "package main ; func f0 ( a bool , b int ) {", "bad")
	r := &parentObserver{badID: "bad"}
	badFrontier := false
	_, err := searchFrom(context.Background(), p, r, Options{Strategy: "beam", Trace: func(e Event) {
		if e.Reason == "frontier" {
			for _, path := range e.Frontier {
				if path.ID == "bad" {
					badFrontier = true
				}
			}
		}
	}}, func(string) (bool, error) { return false, nil }, []*State{bad})
	if !errors.Is(err, ErrNoSolution) || r.calls != 0 || badFrontier {
		t.Fatalf("%v calls=%d bad_frontier=%v", err, r.calls, badFrontier)
	}
}
func TestCancelledBeforeInitialDeclarationGate(t *testing.T) {
	p := subtractionSpec(t)
	bad := replayPrefix(t, "package main ; func f0 ( a bool , b int ) {", "bad")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	r := &parentObserver{badID: "bad"}
	_, err := searchFrom(ctx, p, r, Options{Strategy: "beam"}, func(string) (bool, error) { return false, nil }, []*State{bad})
	if !errors.Is(err, context.Canceled) || r.calls != 0 {
		t.Fatalf("err=%v calls=%d", err, r.calls)
	}
}
func TestFailedCompletedCandidateKeepsSibling(t *testing.T) {
	p := subtractionSpec(t)
	prefix := replayPrefix(t, "package main ; func f0 ( a int , b int ) int { return a", "good")
	target := "package main ; func f0 ( a int , b int ) int { return a - b ; }"
	failed := 0
	out, err := searchFrom(context.Background(), p, scripted(target), Options{Strategy: "beam"}, func(s string) (bool, error) {
		if s != target {
			failed++
		}
		return s == target, nil
	}, []*State{prefix})
	if err != nil || out.Source != target || failed == 0 {
		t.Fatalf("%+v err=%v failed=%d", out, err, failed)
	}
}
func TestCandidatesExcludeExtraParameterTypeBeforeModel(t *testing.T) {
	p := subtractionSpec(t)
	s := replayPrefix(t, "package main ; func f0 ( a int , b int ,", "prefix")
	for _, c := range mustNext(t, p, s) {
		if c.Lexeme == "int" {
			t.Fatalf("extra type leaked: %+v", mustNext(t, p, s))
		}
	}
}

type adversarialRanker struct {
	target     string
	sawInvalid bool
}

func (r *adversarialRanker) Rank(_ context.Context, state string, cs []rank.Candidate) ([]rank.Score, error) {
	var payload struct {
		Parents map[string]struct {
			Prefix string `json:"prefix"`
		} `json:"parents"`
	}
	if err := json.Unmarshal([]byte(state), &payload); err != nil {
		return nil, err
	}
	out := make([]rank.Score, len(cs))
	for i, c := range cs {
		prefix := payload.Parents[c.ParentID].Prefix
		if strings.HasSuffix(prefix, "a int , b int ,") && c.Lexeme == "int" {
			r.sawInvalid = true
		}
		candidate := strings.TrimSpace(prefix + " " + c.Lexeme)
		score := 0.0
		if strings.HasPrefix(r.target, candidate) {
			score = 1000 + float64(len(candidate))
		}
		if r.sawInvalid && c.Lexeme == "int" {
			score = 100000
		}
		out[i] = rank.Score{ID: c.ID, Score: score}
	}
	return out, nil
}
func TestAdversarialPreferenceCannotSelectExtraParameter(t *testing.T) {
	p := subtractionSpec(t)
	target := "package main ; func f0 ( a int , b int , ) int { return a - b ; }"
	r := &adversarialRanker{target: target}
	rejected := false
	out, err := Search(context.Background(), p, r, Options{Strategy: "strict-greedy", Trace: func(e Event) {
		if e.Reason == "declaration_rejected" && e.Lexeme == "int" && e.RejectionReason == "parameter_count" {
			rejected = true
		}
	}}, func(s string) (bool, error) { return s == target, nil })
	if err != nil || out.Source != target || r.sawInvalid || !rejected {
		t.Fatalf("source=%q invalid=%v rejected=%v err=%v", out.Source, r.sawInvalid, rejected, err)
	}
}
func TestRejectedEdgesConsumeCandidateBudget(t *testing.T) {
	p := subtractionSpec(t)
	s := replayPrefix(t, "package main ; func f0 ( a int , b int ,", "prefix")
	r := &parentObserver{badID: ""}
	result, err := searchFrom(context.Background(), p, r, Options{Strategy: "beam", MaxCandidates: 1}, func(string) (bool, error) { return false, nil }, []*State{s})
	if !errors.Is(err, ErrBudget) || result.Candidates != 1 || r.calls != 0 {
		t.Fatalf("%+v err=%v calls=%d", result, err, r.calls)
	}
}
