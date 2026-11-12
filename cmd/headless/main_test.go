package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"tessaro/internal/application"
	"tessaro/internal/compare"
	"tessaro/internal/rank"
	"tessaro/internal/runner"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestInspectLegacyHistoryWithoutResolver(t *testing.T) {
	dir := t.TempDir()
	id := "2026-11-07.001"
	if err := os.WriteFile(filepath.Join(dir, id), []byte(`{"engine":"token","candidate_unit":"token","status":"completed"}`), 0600); err != nil {
		t.Fatal(err)
	}
	var out, stderr bytes.Buffer
	code := execute(context.Background(), []string{"inspect", "--output", dir, "--run", id, "--json"}, &out, &stderr, nil)
	var record struct {
		ID   string `json:"id"`
		Kind string `json:"kind"`
	}
	if code != 0 || json.Unmarshal(out.Bytes(), &record) != nil || record.ID != id || record.Kind != "synthesis" {
		t.Fatalf("code=%d out=%s stderr=%s", code, out.String(), stderr.String())
	}
}

func TestRunRejectsMissingBudgetBeforeModel(t *testing.T) {
	var out, stderr bytes.Buffer
	code := execute(context.Background(), []string{"run", "--spec", "missing.json", "--model", "jev", "--json"}, &out, &stderr, nil)
	if code == 0 {
		t.Fatalf("unbounded paid run accepted: %s", out.String())
	}
}

type blocking struct{ entered chan struct{} }

func (b *blocking) Rank(ctx context.Context, _ string, _ []rank.Candidate) ([]rank.Score, error) {
	select {
	case b.entered <- struct{}{}:
	default:
	}
	<-ctx.Done()
	return nil, ctx.Err()
}

func TestRunCancellationDrainsAndPersistsTrace(t *testing.T) {
	dir := t.TempDir()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	r := &blocking{entered: make(chan struct{}, 1)}
	var out, stderr bytes.Buffer
	done := make(chan int, 1)
	path, err := filepath.Abs("../../examples/token/specs/loop-factorial.json")
	if err != nil {
		t.Fatal(err)
	}
	go func() {
		done <- execute(ctx, []string{"run", "--spec", path, "--problem", "factorial", "--model", "jev", "--max-questions", "3", "--output", dir, "--progress", "--json"}, &out, &stderr, func(context.Context, compare.Model) (rank.Ranker, error) { return r, nil })
	}()
	select {
	case <-r.entered:
	case <-time.After(15 * time.Second):
		t.Fatal("ranker not reached")
	}
	cancel()
	select {
	case code := <-done:
		if code == 0 {
			t.Fatal("cancelled run returned success")
		}
	case <-time.After(15 * time.Second):
		t.Fatal("cancellation did not finish")
	}
	var events []application.Envelope
	var summary struct {
		Status string `json:"status"`
		RunID  string `json:"run_id"`
	}
	for _, line := range bytes.Split(bytes.TrimSpace(out.Bytes()), []byte("\n")) {
		var row map[string]json.RawMessage
		if json.Unmarshal(line, &row) != nil {
			t.Fatalf("invalid JSONL: %s", line)
		}
		var kind string
		_ = json.Unmarshal(row["kind"], &kind)
		if kind == "summary" {
			_ = json.Unmarshal(line, &summary)
		} else {
			var ev application.Envelope
			_ = json.Unmarshal(line, &ev)
			events = append(events, ev)
		}
	}
	if summary.Status != "cancelled" || summary.RunID == "" || len(events) == 0 || events[len(events)-1].Kind != "run_finished" {
		t.Fatalf("summary=%+v events=%d stderr=%s", summary, len(events), stderr.String())
	}
	for i, ev := range events {
		if ev.Seq != uint64(i+1) {
			t.Fatalf("event sequence at %d: %d", i, ev.Seq)
		}
	}
	m := application.New(application.Config{OutputDir: dir})
	saved, e := m.ReadHistory(summary.RunID)
	if e != nil || saved.Status != "cancelled" {
		t.Fatalf("saved=%+v err=%v", saved, e)
	}
	trace, e := m.ReadTrace(summary.RunID)
	if e != nil || len(trace) != len(events) {
		t.Fatalf("persisted=%d streamed=%d err=%v", len(trace), len(events), e)
	}
}

func TestCompareRejectsDifferentBudgets(t *testing.T) {
	dir := t.TempDir()
	for i, b := range []int{1, 2} {
		id := fmt.Sprintf("2026-11-07.%03d", i+1)
		data := fmt.Sprintf(`{"kind":"desktop_run","run_id":%q,"status":"completed","results":[{"result":{"engine":"token","input_hash":"same","effective_budgets":{"max_calls":%d}}}]}`, id, b)
		if err := os.WriteFile(filepath.Join(dir, id), []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
	}
	var out, stderr bytes.Buffer
	if execute(context.Background(), []string{"compare", "--output", dir, "--run", "2026-11-07.001", "--run", "2026-11-07.002"}, &out, &stderr, nil) == 0 {
		t.Fatal("mismatched budgets accepted")
	}
}

func TestQuestionGateLimitsAcrossResolvedRankers(t *testing.T) {
	gate := &questionGate{limit: 1}
	first := gate.wrap(rank.Fake{})
	second := gate.wrap(rank.Fake{})
	cs := []rank.Candidate{{ID: "a"}}
	if _, err := first.Rank(context.Background(), "", cs); err != nil {
		t.Fatal(err)
	}
	if _, err := second.Rank(context.Background(), "", cs); err == nil {
		t.Fatal("shared budget exceeded")
	}
}

func TestNoSolutionIsNotSuccessfulExit(t *testing.T) {
	dir := t.TempDir()
	path, err := filepath.Abs("../../examples/token/specs/loop-factorial.json")
	if err != nil {
		t.Fatal(err)
	}
	var out, stderr bytes.Buffer
	code := execute(context.Background(), []string{"run", "--spec", path, "--max-questions", "1", "--max-calls", "1", "--output", dir, "--json"}, &out, &stderr, func(context.Context, compare.Model) (rank.Ranker, error) { return rank.Fake{}, nil })
	var summary struct {
		Status  string                      `json:"status"`
		Results []application.ProblemResult `json:"results"`
	}
	if err := json.Unmarshal(out.Bytes(), &summary); err != nil {
		t.Fatalf("output=%s error=%v", out.String(), err)
	}
	if code == 0 || summary.Status != "completed" || len(summary.Results) != 1 || summary.Results[0].Result.Status == "success" {
		t.Fatalf("code=%d summary=%+v stderr=%s", code, summary, stderr.String())
	}
}

func TestComparisonReportsFailedTrialsWithNonzeroExit(t *testing.T) {
	dir := t.TempDir()
	spec, err := filepath.Abs("../../examples/token/specs/loop-factorial.json")
	if err != nil {
		t.Fatal(err)
	}
	manifest := compare.Manifest{Engine: "token", Models: []compare.Model{{ID: "fixture", Ranker: "jev"}}, Problems: []compare.Problem{{ID: "p", Spec: spec}}, Strategies: []string{"beam"}, Repeats: 1, Seed: 42}
	data, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "manifest.json")
	if err = os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	var out, stderr bytes.Buffer
	code := execute(context.Background(), []string{"run", "--kind", "compare", "--manifest", path, "--max-questions", "1", "--max-calls", "1", "--output", dir, "--json"}, &out, &stderr, func(context.Context, compare.Model) (rank.Ranker, error) { return rank.Fake{}, nil })
	if code == 0 {
		t.Fatalf("failed trial returned success: %s", out.String())
	}
}

func TestLocalModelRequiresExplicitRuntimePaths(t *testing.T) {
	var out, stderr bytes.Buffer
	code := execute(context.Background(), []string{"run", "--spec", "missing", "--model", "local", "--max-questions", "1", "--python", "python", "--json"}, &out, &stderr, nil)
	if code == 0 || !strings.Contains(stderr.String(), "local_runtime_not_configured") {
		t.Fatalf("code=%d stderr=%s", code, stderr.String())
	}
}

func TestHelpSucceeds(t *testing.T) {
	var out, stderr bytes.Buffer
	if code := execute(context.Background(), []string{"--help"}, &out, &stderr, nil); code != 0 {
		t.Fatalf("code=%d stderr=%s", code, stderr.String())
	}
}

func TestJevUsesExpectedCredentialEnvironmentName(t *testing.T) {
	var got compare.Model
	path, err := filepath.Abs("../../examples/token/specs/loop-factorial.json")
	if err != nil {
		t.Fatal(err)
	}
	var out, stderr bytes.Buffer
	execute(context.Background(), []string{"run", "--spec", path, "--output", t.TempDir(), "--max-questions", "1", "--max-calls", "1"}, &out, &stderr, func(_ context.Context, m compare.Model) (rank.Ranker, error) { got = m; return rank.Fake{}, nil })
	if got.KeyEnv != "TYPESAFE_API_KEY" {
		t.Fatalf("credential environment name is %q", got.KeyEnv)
	}
}

func TestExplicitZeroLocalBudgetIsPreserved(t *testing.T) {
	path, err := filepath.Abs("../../examples/token/specs/loop-factorial.json")
	if err != nil {
		t.Fatal(err)
	}
	var out, stderr bytes.Buffer
	execute(context.Background(), []string{"run", "--spec", path, "--output", t.TempDir(), "--max-questions", "1", "--max-calls", "1", "--max-locals", "0", "--json"}, &out, &stderr, func(context.Context, compare.Model) (rank.Ranker, error) { return rank.Fake{}, nil })
	var summary struct {
		Results []application.ProblemResult `json:"results"`
	}
	if err := json.Unmarshal(out.Bytes(), &summary); err != nil || len(summary.Results) != 1 {
		t.Fatalf("result %s err %v", out.String(), err)
	}
	if summary.Results[0].Result.EffectiveBudgets["max_locals"] != float64(0) {
		t.Fatalf("zero limit lost: %+v", summary.Results[0].Result.EffectiveBudgets)
	}
}

func TestComparisonRequiresMatchingSeedAndReturnPolicy(t *testing.T) {
	tests := []struct {
		name     string
		seed     int
		policy   string
		strategy string
		want     int
	}{{"different seed", 43, "values-only-v1", "beam", 2}, {"different policy", 42, "legacy", "beam", 2}, {"different strategy allowed", 42, "values-only-v1", "lds", 0}}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			for i, entry := range []struct {
				seed             int
				policy, strategy string
			}{{42, "values-only-v1", "beam"}, {tc.seed, tc.policy, tc.strategy}} {
				id := fmt.Sprintf("2026-11-07.%03d", i+1)
				data := fmt.Sprintf(`{"kind":"desktop_run","run_id":%q,"status":"completed","results":[{"result":{"engine":"token","input_hash":"same","seed":%d,"return_policy":%q,"strategy":%q,"effective_budgets":{"max_calls":1}}}]}`, id, entry.seed, entry.policy, entry.strategy)
				if err := os.WriteFile(filepath.Join(dir, id), []byte(data), 0600); err != nil {
					t.Fatal(err)
				}
			}
			var out, stderr bytes.Buffer
			code := execute(context.Background(), []string{"compare", "--output", dir, "--run", "2026-11-07.001", "--run", "2026-11-07.002"}, &out, &stderr, nil)
			if code != tc.want {
				t.Fatalf("code=%d stderr=%s", code, stderr.String())
			}
		})
	}
}

type batchFixture struct {
	calls     int
	questions int
}

func (b *batchFixture) Rank(context.Context, string, []rank.Candidate) ([]rank.Score, error) {
	return nil, nil
}
func (b *batchFixture) SupportsDecisionBatch() bool { return true }
func (b *batchFixture) HTTPRequests() int64         { return int64(b.calls) }
func (b *batchFixture) RankDecisions(_ context.Context, qs []rank.DecisionRequest) ([]rank.Decision, *rank.Usage, error) {
	b.calls++
	b.questions += len(qs)
	n := 5
	return make([]rank.Decision, len(qs)), &rank.Usage{InputTokens: &n}, nil
}
func TestBatchBudgetAndCapabilities(t *testing.T) {
	b := &batchFixture{}
	g := (&questionGate{limit: 2}).wrap(b)
	counter, ok := g.(rank.HTTPRequestCounter)
	if !ok {
		t.Fatal("HTTP request counter lost")
	}
	if _, ok := (&questionGate{limit: 1}).wrap(rank.Fake{}).(rank.HTTPRequestCounter); ok {
		t.Fatal("unknown HTTP count reported as known")
	}
	batch, ok := g.(rank.BatchDecisionRanker)
	if !ok {
		t.Fatal("batch capability lost")
	}
	if !g.(rank.BatchCapability).SupportsDecisionBatch() {
		t.Fatal("batch support lost")
	}
	qs := []rank.DecisionRequest{{}, {}}
	ds, u, err := batch.RankDecisions(context.Background(), qs)
	if err != nil || len(ds) != 2 || u == nil || *u.InputTokens != 5 || counter.HTTPRequests() != 1 {
		t.Fatalf("decisions=%v usage=%v err=%v", ds, u, err)
	}
	_, _, err = batch.RankDecisions(context.Background(), qs)
	var safe interface{ ErrorCode() string }
	if !errors.As(err, &safe) || safe.ErrorCode() != "question_budget_exhausted" || b.questions != 2 {
		t.Fatalf("budget err=%v sent=%d", err, b.questions)
	}
}
func TestCompareRejectsMissingResearchMetadata(t *testing.T) {
	dir := t.TempDir()
	for i, extra := range []string{`"seed":0,"return_policy":"values-only-v1",`, `"return_policy":"values-only-v1",`} {
		id := fmt.Sprintf("2026-11-07.%03d", i+1)
		data := fmt.Sprintf(`{"kind":"desktop_run","run_id":%q,"status":"completed","results":[{"result":{%s"engine":"token","input_hash":"same","effective_budgets":{}}}]}`, id, extra)
		if e := os.WriteFile(filepath.Join(dir, id), []byte(data), 0600); e != nil {
			t.Fatal(e)
		}
	}
	var out, stderr bytes.Buffer
	if execute(context.Background(), []string{"compare", "--output", dir, "--run", "2026-11-07.001", "--run", "2026-11-07.002"}, &out, &stderr, nil) == 0 {
		t.Fatal("missing seed accepted")
	}
}
func TestRejectsUnsupportedMCTSBatchBeforeResolver(t *testing.T) {
	var out, stderr bytes.Buffer
	called := false
	code := execute(context.Background(), []string{"run", "--spec", "missing", "--strategy", "mcts", "--decision-batch-size", "2", "--max-questions", "2"}, &out, &stderr, func(context.Context, compare.Model) (rank.Ranker, error) { called = true; return rank.Fake{}, nil })
	if code == 0 || called || !strings.Contains(stderr.String(), "batch") {
		t.Fatalf("code=%d called=%v stderr=%s", code, called, stderr.String())
	}
}

func TestAuditReplaysOnlySavedTokenChoices(t *testing.T) {
	dir := t.TempDir()
	id := "2026-11-07.001"
	path := filepath.Join(dir, id+".trace.jsonl")
	snapshot := fmt.Sprintf(`{"kind":"desktop_run","run_id":%q,"status":"completed","results":[{"result":{"return_policy":"values-only-v1","ranker":{"model":"jev-1.14.0","returned_model":"jev-1.14.0"}}}]}`, id)
	if e := os.WriteFile(filepath.Join(dir, id), []byte(snapshot), 0600); e != nil {
		t.Fatal(e)
	}
	event := func() string {
		cs := []rank.Candidate{{ID: "a", ParentID: "p", TokenKind: "IDENT", Lexeme: "x"}, {ID: "b", ParentID: "p", TokenKind: "INT", Lexeme: "1"}}
		instructions, criteria, e := rank.ChoiceQuestion(cs)
		if e != nil {
			t.Fatal(e)
		}
		stateJSON := `{"intent":"fixture","parents":{"p":{"prefix":"return"}},"public_tests":[],"return_policy":"values-only-v1"}`
		data, e := json.Marshal(map[string]any{"seq": 3, "run_id": id, "kind": "search", "data": map[string]any{"reason": "decision_context", "decision_context": map[string]any{"intent": "fixture", "return_policy": "values-only-v1", "public_tests": []any{}, "parents": map[string]any{"p": map[string]any{"prefix": "return"}}, "instructions": instructions, "criteria": criteria, "transport": "single", "state_json": stateJSON, "schema_version": "decision-context-v1", "ordered_candidates": cs}}})
		if e != nil {
			t.Fatal(e)
		}
		return string(data)
	}()
	if err := os.WriteFile(path, []byte(event+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	seen := 0
	var out, stderr bytes.Buffer
	code := execute(context.Background(), []string{"audit", "--trace", path, "--seq", "3", "--max-questions", "7"}, &out, &stderr, func(_ context.Context, m compare.Model) (rank.Ranker, error) {
		if m.Model != "jev-1.14.0" {
			t.Fatalf("model=%s", m.Model)
		}
		return auditFixture{seen: &seen}, nil
	})
	if code != 0 || seen != 7 {
		t.Fatalf("code=%d calls=%d stderr=%s", code, seen, stderr.String())
	}
	var result struct {
		Observations []any `json:"observations"`
	}
	if e := json.Unmarshal(out.Bytes(), &result); e != nil || len(result.Observations) != 7 {
		t.Fatalf("output=%s err=%v", out.String(), e)
	}
}

type auditFixture struct{ seen *int }

func (a auditFixture) Rank(_ context.Context, _ string, cs []rank.Candidate) ([]rank.Score, error) {
	*a.seen++
	ss := make([]rank.Score, len(cs))
	for i, c := range cs {
		if c.ParentID != "p" || (c.Lexeme != "x" && c.Lexeme != "1") {
			panic("unexpected choice")
		}
		ss[i] = rank.Score{ID: c.ID, Score: 1 / float64(len(cs))}
	}
	return ss, nil
}
func (a auditFixture) RankDecision(ctx context.Context, state string, cs []rank.Candidate) (rank.Decision, error) {
	s, e := a.Rank(ctx, state, cs)
	return rank.Decision{Scores: s, ChosenID: cs[0].ID}, e
}
func (a auditFixture) NormalizedCategorical() bool { return true }
func TestAuditRejectsTraceWithoutExactRequestState(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "trace.jsonl")
	cs := []rank.Candidate{{ID: "a", ParentID: "p", TokenKind: "IDENT", Lexeme: "x"}, {ID: "b", ParentID: "p", TokenKind: "INT", Lexeme: "1"}}
	ins, criteria, _ := rank.ChoiceQuestion(cs)
	data, _ := json.Marshal(map[string]any{"seq": 1, "kind": "search", "data": map[string]any{"reason": "decision_context", "decision_context": map[string]any{"intent": "f", "public_tests": []any{}, "parents": map[string]any{"p": map[string]any{"prefix": "return"}}, "instructions": ins, "criteria": criteria}}})
	if e := os.WriteFile(path, append(data, '\n'), 0600); e != nil {
		t.Fatal(e)
	}
	called := false
	var out, stderr bytes.Buffer
	code := execute(context.Background(), []string{"audit", "--trace", path, "--seq", "1", "--max-questions", "7"}, &out, &stderr, func(context.Context, compare.Model) (rank.Ranker, error) { called = true; return rank.Fake{}, nil })
	if code == 0 || called {
		t.Fatalf("code=%d called=%v stderr=%s", code, called, stderr.String())
	}
}
func TestAuditRequiresSourceModelProvenance(t *testing.T) {
	// A valid question without a matching saved result must never trigger a model call.
	dir := t.TempDir()
	path := filepath.Join(dir, "trace.jsonl")
	cs := []rank.Candidate{{ID: "a", ParentID: "p", TokenKind: "IDENT", Lexeme: "x"}, {ID: "b", ParentID: "p", TokenKind: "INT", Lexeme: "1"}}
	ins, criteria, _ := rank.ChoiceQuestion(cs)
	state := `{"intent":"f","public_tests":[],"parents":{"p":{"prefix":"return"}}}`
	var contextData map[string]any
	_ = json.Unmarshal([]byte(state), &contextData)
	contextData["instructions"] = ins
	contextData["criteria"] = criteria
	contextData["state_json"] = state
	contextData["transport"] = "single"
	data, _ := json.Marshal(map[string]any{"seq": 1, "kind": "search", "data": map[string]any{"reason": "decision_context", "decision_context": contextData}})
	if e := os.WriteFile(path, append(data, '\n'), 0600); e != nil {
		t.Fatal(e)
	}
	called := false
	var out, stderr bytes.Buffer
	execute(context.Background(), []string{"audit", "--trace", path, "--seq", "1", "--max-questions", "7"}, &out, &stderr, func(context.Context, compare.Model) (rank.Ranker, error) {
		called = true
		return auditFixture{seen: new(int)}, nil
	})
	if called {
		t.Fatal("resolver called without provenance")
	}
}
func (a auditFixture) Metadata() rank.Metadata {
	return rank.Metadata{Model: "jev-1.14.0", ReturnedModel: "jev-1.14.0"}
}
func TestBatchBudgetRejectsAllBeforeProvider(t *testing.T) {
	inner := &batchFixture{}
	wrapped := (&questionGate{limit: 1}).wrap(inner)
	batch := wrapped.(rank.BatchDecisionRanker)
	_, _, err := batch.RankDecisions(context.Background(), []rank.DecisionRequest{{}, {}})
	var safe interface{ ErrorCode() string }
	if !errors.As(err, &safe) || safe.ErrorCode() != "question_budget_exhausted" || inner.calls != 0 || inner.questions != 0 || wrapped.(rank.HTTPRequestCounter).HTTPRequests() != 0 {
		t.Fatalf("err=%v calls=%d questions=%d", err, inner.calls, inner.questions)
	}
}
func TestHelpDocumentsAuditAndBatch(t *testing.T) {
	var out, stderr bytes.Buffer
	if code := execute(context.Background(), []string{"--help"}, &out, &stderr, nil); code != 0 || !strings.Contains(out.String(), "audit") || !strings.Contains(out.String(), "--decision-batch-size") {
		t.Fatalf("code=%d help=%s", code, out.String())
	}
}
func TestAuditRejectsUnknownReturnProfile(t *testing.T) {
	dir := t.TempDir()
	id := "2026-11-07.001"
	path := filepath.Join(dir, id+".trace.jsonl")
	snapshot := fmt.Sprintf(`{"kind":"desktop_run","run_id":%q,"status":"completed","results":[{"result":{"return_policy":"values-only-v1","ranker":{"model":"jev-1.14.0","returned_model":"jev-1.14.0"}}}]}`, id)
	if e := os.WriteFile(filepath.Join(dir, id), []byte(snapshot), 0600); e != nil {
		t.Fatal(e)
	}
	cs := []rank.Candidate{{ID: "a", ParentID: "p", TokenKind: "IDENT", Lexeme: "x"}, {ID: "b", ParentID: "p", TokenKind: "INT", Lexeme: "1"}}
	ins, criteria, _ := rank.ChoiceQuestion(cs)
	state := `{"intent":"f","public_tests":[],"parents":{"p":{"prefix":"return"}}}`
	var c map[string]any
	_ = json.Unmarshal([]byte(state), &c)
	c["instructions"] = ins
	c["criteria"] = criteria
	c["state_json"] = state
	c["transport"] = "single"
	data, _ := json.Marshal(map[string]any{"seq": 1, "run_id": id, "kind": "search", "data": map[string]any{"reason": "decision_context", "decision_context": c}})
	if e := os.WriteFile(path, append(data, '\n'), 0600); e != nil {
		t.Fatal(e)
	}
	called := false
	var out, stderr bytes.Buffer
	execute(context.Background(), []string{"audit", "--trace", path, "--seq", "1", "--max-questions", "7"}, &out, &stderr, func(context.Context, compare.Model) (rank.Ranker, error) {
		called = true
		return auditFixture{seen: new(int)}, nil
	})
	if called {
		t.Fatal("unknown profile allowed")
	}
}

func TestAuditRoundTripManagerProducedDecision(t *testing.T) {
	for _, strategy := range []string{"strict-greedy", "beam", "lds"} {
		t.Run(strategy, func(t *testing.T) {
			dir := t.TempDir()
			path, err := filepath.Abs("../../examples/token/specs/loop-factorial.json")
			if err != nil {
				t.Fatal(err)
			}
			sourceCalls := 0
			mgr := application.New(application.Config{OutputDir: dir, Resolve: func(context.Context, compare.Model) (rank.Ranker, error) {
				return roundTripRanker{seen: &sourceCalls}, nil
			}})
			id, err := mgr.Start(application.Request{Kind: "search", Engine: "token", Strategy: strategy, SpecPath: path, ProblemID: "factorial", Model: compare.Model{ID: "jev", Ranker: "jev"}, Options: runner.Request{MaxCalls: 1, ReturnPolicy: "values-only-v1", Seed: 42}})
			if err != nil {
				t.Fatal(err)
			}
			deadline := time.After(30 * time.Second)
			for {
				snap := mgr.Snapshot()
				if snap.Status != "running" && snap.Status != "cancelling" {
					break
				}
				select {
				case <-mgr.Changed():
				case <-deadline:
					t.Fatal("source search timeout")
				}
			}
			events, err := mgr.ReadTrace(id)
			if err != nil {
				t.Fatal(err)
			}
			var seq uint64
			for _, ev := range events {
				var event struct {
					Reason string `json:"reason"`
					Phase  string `json:"phase"`
				}
				if ev.Kind == "search" && json.Unmarshal(ev.Data, &event) == nil && event.Reason == "decision_context" && event.Phase == "" {
					seq = ev.Seq
					break
				}
			}
			if seq == 0 {
				t.Fatal("source decision missing")
			}
			calls := 0
			var out, stderr bytes.Buffer
			code := execute(context.Background(), []string{"audit", "--trace", filepath.Join(dir, id+".trace.jsonl"), "--seq", fmt.Sprint(seq), "--max-questions", "7"}, &out, &stderr, func(context.Context, compare.Model) (rank.Ranker, error) { return roundTripRanker{seen: &calls}, nil })
			if code != 0 || calls != 7 {
				t.Fatalf("strategy=%s code=%d calls=%d stderr=%s", strategy, code, calls, stderr.String())
			}
			if strategy == "lds" {
				tracePath := filepath.Join(dir, id+".trace.jsonl")
				original, err := os.ReadFile(tracePath)
				if err != nil {
					t.Fatal(err)
				}
				for _, mutation := range []string{"candidate", "unknown_metadata"} {
					lines := bytes.Split(original, []byte("\n"))
					for i, line := range lines {
						var row map[string]any
						if json.Unmarshal(line, &row) != nil || row["seq"] != float64(seq) {
							continue
						}
						data := row["data"].(map[string]any)
						context := data["decision_context"].(map[string]any)
						if mutation == "candidate" {
							cs := context["ordered_candidates"].([]any)
							cs[0].(map[string]any)["lexeme"] = "invalid"
						} else {
							context["unrecognized_metadata"] = true
						}
						lines[i], err = json.Marshal(row)
						if err != nil {
							t.Fatal(err)
						}
					}
					if err = os.WriteFile(tracePath, bytes.Join(lines, []byte("\n")), 0600); err != nil {
						t.Fatal(err)
					}
					calls = 0
					out.Reset()
					stderr.Reset()
					code = execute(context.Background(), []string{"audit", "--trace", tracePath, "--seq", fmt.Sprint(seq), "--max-questions", "7"}, &out, &stderr, func(context.Context, compare.Model) (rank.Ranker, error) { return roundTripRanker{seen: &calls}, nil })
					if code == 0 || calls != 0 {
						t.Fatalf("%s was accepted: code=%d calls=%d", mutation, code, calls)
					}
				}
				if err = os.WriteFile(tracePath, original, 0600); err != nil {
					t.Fatal(err)
				}
			}
		})
	}
}

type roundTripRanker struct{ seen *int }

func (r roundTripRanker) Rank(ctx context.Context, state string, cs []rank.Candidate) ([]rank.Score, error) {
	d, e := r.RankDecision(ctx, state, cs)
	return d.Scores, e
}
func (r roundTripRanker) RankDecision(_ context.Context, _ string, cs []rank.Candidate) (rank.Decision, error) {
	*r.seen++
	scores := make([]rank.Score, len(cs))
	for i, c := range cs {
		scores[i] = rank.Score{ID: c.ID, Score: 1 / float64(len(cs))}
	}
	return rank.Decision{Scores: scores, ChosenID: cs[0].ID}, nil
}
func (roundTripRanker) Metadata() rank.Metadata {
	return rank.Metadata{Model: "jev-1.14.0", ReturnedModel: "jev-1.14.0"}
}
func (roundTripRanker) NormalizedCategorical() bool { return true }
func TestAuditHTTPRoundedDistributionAndPartialFailure(t *testing.T) {
	for _, failAt := range []int{0, 3} {
		t.Run(fmt.Sprint(failAt), func(t *testing.T) {
			dir := t.TempDir()
			id := "2026-11-07.001"
			path := filepath.Join(dir, id+".trace.jsonl")
			snap := fmt.Sprintf(`{"kind":"desktop_run","run_id":%q,"status":"completed","results":[{"result":{"return_policy":"values-only-v1","ranker":{"model":"jev-1.14.0","returned_model":"jev-1.14.0"}}}]}`, id)
			if e := os.WriteFile(filepath.Join(dir, id), []byte(snap), 0600); e != nil {
				t.Fatal(e)
			}
			cs := []rank.Candidate{{ID: "c", ParentID: "p", TokenKind: "INT", Lexeme: "2"}, {ID: "b", ParentID: "p", TokenKind: "INT", Lexeme: "1"}, {ID: "a", ParentID: "p", TokenKind: "IDENT", Lexeme: "x"}}
			ins, criteria, _ := rank.ChoiceQuestion(cs)
			state := `{"intent":"f","public_tests":[],"parents":{"p":{"prefix":"return"}},"return_policy":"values-only-v1"}`
			var c map[string]any
			_ = json.Unmarshal([]byte(state), &c)
			c["instructions"] = ins
			c["criteria"] = criteria
			c["state_json"] = state
			c["transport"] = "single"
			c["schema_version"] = "decision-context-v1"
			c["ordered_candidates"] = cs
			data, _ := json.Marshal(map[string]any{"seq": 1, "run_id": id, "kind": "search", "data": map[string]any{"reason": "decision_context", "decision_context": c}})
			if e := os.WriteFile(path, append(data, '\n'), 0600); e != nil {
				t.Fatal(e)
			}
			calls := 0
			var orders [][]string
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				if calls == failAt {
					w.WriteHeader(503)
					return
				}
				var req struct {
					Questions map[string]struct {
						Criteria map[string]any `json:"criteria"`
					} `json:"questions"`
				}
				if e := json.NewDecoder(r.Body).Decode(&req); e != nil {
					t.Error(e)
					return
				}
				keys := make([]string, 0, len(req.Questions["pick"].Criteria))
				for key := range req.Questions["pick"].Criteria {
					keys = append(keys, key)
				}
				sort.Strings(keys)
				order := []string{}
				for _, key := range keys {
					order = append(order, req.Questions["pick"].Criteria[key].(map[string]any)["lexeme"].(string))
				}
				orders = append(orders, order)
				scores := map[string]float64{}
				choice := ""
				for k := range req.Questions["pick"].Criteria {
					scores[k] = .3333
					choice = k
				}
				_ = json.NewEncoder(w).Encode(map[string]any{"model": "jev-1.14.0", "usage": map[string]int{"input_tokens": 5, "output_tokens": 2}, "answers": map[string]any{"pick": map[string]any{"type": "choice", "choice": choice, "probabilities": scores}}})
			}))
			defer server.Close()
			var out, stderr bytes.Buffer
			code := execute(context.Background(), []string{"audit", "--trace", path, "--seq", "1", "--max-questions", "7"}, &out, &stderr, func(context.Context, compare.Model) (rank.Ranker, error) {
				return &rank.HTTP{URL: server.URL, Model: "jev-1.14.0"}, nil
			})
			var result struct {
				Status       string             `json:"status"`
				Completed    int                `json:"completed"`
				Observations []auditObservation `json:"observations"`
			}
			if e := json.Unmarshal(out.Bytes(), &result); e != nil {
				t.Fatalf("code=%d output=%s stderr=%s", code, out.String(), stderr.String())
			}
			if failAt == 0 {
				if code != 0 || calls != 7 || result.Completed != 7 {
					t.Fatalf("code=%d calls=%d result=%+v", code, calls, result)
				}
				for i := 1; i <= 3; i++ {
					if !reflect.DeepEqual(orders[0], orders[i]) {
						t.Fatalf("id-only variant changed wire order: %v", orders)
					}
				}
				for _, o := range result.Observations {
					sum := 0.0
					for _, p := range o.Scores {
						sum += p
					}
					if math.Abs(sum-1) > 1e-9 {
						t.Fatalf("sum=%g", sum)
					}
				}
			} else if code == 0 || calls != 3 || result.Completed != 2 || len(result.Observations) != 2 || result.Status != "failed" {
				t.Fatalf("code=%d calls=%d result=%+v", code, calls, result)
			}
		})
	}
}

type localAuditRanker struct {
	calls  *int
	failAt int
}

func (r localAuditRanker) Metadata() rank.Metadata {
	return rank.Metadata{Model: "jev-1.14.0", ReturnedModel: "jev-1.14.0"}
}
func (r localAuditRanker) NormalizedCategorical() bool { return true }
func (r localAuditRanker) Rank(ctx context.Context, s string, cs []rank.Candidate) ([]rank.Score, error) {
	d, e := r.RankDecision(ctx, s, cs)
	return d.Scores, e
}
func (r localAuditRanker) RankDecision(_ context.Context, state string, cs []rank.Candidate) (rank.Decision, error) {
	*r.calls++
	if r.failAt > 0 && *r.calls == r.failAt {
		return rank.Decision{}, fmt.Errorf("fixture failure")
	}
	scores := make([]rank.Score, len(cs))
	choice := ""
	high := -1.0
	for i, c := range cs {
		v := 0.0
		if len(cs) > 2 {
			switch c.Lexeme {
			case "a":
				v = .4
			case "b":
				v = .1
			case "e":
				v = .5
			}
		} else {
			var st struct {
				Parents map[string]any `json:"parents"`
			}
			_ = json.Unmarshal([]byte(state), &st)
			if len(st.Parents) != 1 {
				return rank.Decision{}, fmt.Errorf("not parent local")
			}
			if c.Lexeme == "a" {
				v = .7
			} else if c.Lexeme == "b" {
				v = .3
			} else {
				v = .5
			}
		}
		scores[i] = rank.Score{ID: c.ID, Score: v}
		if v > high {
			high = v
			choice = c.ID
		}
	}
	return rank.Decision{Scores: scores, ChosenID: choice}, nil
}
func TestAuditParentLocalControlledComparison(t *testing.T) {
	dir := t.TempDir()
	id := "2026-11-07.001"
	path := filepath.Join(dir, id+".trace.jsonl")
	snapshot := fmt.Sprintf(`{"kind":"desktop_run","run_id":%q,"status":"completed","results":[{"result":{"return_policy":"values-only-v1","ranker":{"model":"jev-1.14.0","returned_model":"jev-1.14.0"}}}]}`, id)
	if err := os.WriteFile(filepath.Join(dir, id), []byte(snapshot), 0600); err != nil {
		t.Fatal(err)
	}
	cs := []rank.Candidate{{ID: "a", ParentID: "p1", TokenKind: "IDENT", Lexeme: "a"}, {ID: "b", ParentID: "p1", TokenKind: "IDENT", Lexeme: "b"}, {ID: "c", ParentID: "p2", TokenKind: "IDENT", Lexeme: "c"}, {ID: "d", ParentID: "p2", TokenKind: "IDENT", Lexeme: "d"}, {ID: "e", ParentID: "p3", TokenKind: "IDENT", Lexeme: "e"}}
	ins, criteria, _ := rank.ChoiceQuestion(cs)
	state := `{"intent":"f","public_tests":[],"parents":{"p1":{"prefix":"x"},"p2":{"prefix":"y"},"p3":{"prefix":"z"}},"return_policy":"values-only-v1"}`
	var c map[string]any
	_ = json.Unmarshal([]byte(state), &c)
	c["instructions"] = ins
	c["criteria"] = criteria
	c["state_json"] = state
	c["transport"] = "single"
	c["schema_version"] = "decision-context-v1"
	c["ordered_candidates"] = cs
	data, _ := json.Marshal(map[string]any{"seq": 1, "run_id": id, "kind": "search", "data": map[string]any{"reason": "decision_context", "decision_context": c}})
	if e := os.WriteFile(path, append(data, '\n'), 0600); e != nil {
		t.Fatal(e)
	}
	for _, budget := range []int{8, 9, -9} {
		calls := 0
		var out, stderr bytes.Buffer
		limit := budget
		failAt := 0
		if budget < 0 {
			limit = -budget
			failAt = 9
		}
		code := execute(context.Background(), []string{"audit", "--trace", path, "--seq", "1", "--parent-local", "--max-questions", strconv.Itoa(limit)}, &out, &stderr, func(context.Context, compare.Model) (rank.Ranker, error) {
			return localAuditRanker{calls: &calls, failAt: failAt}, nil
		})
		if budget == 8 {
			if code == 0 || calls != 0 {
				t.Fatalf("budget gate code=%d calls=%d", code, calls)
			}
			continue
		}
		var result struct {
			Completed int `json:"completed"`
			Parents   []struct {
				ParentID       string   `json:"parent_id"`
				Forced         bool     `json:"forced"`
				TotalVariation *float64 `json:"total_variation"`
				Agreement      *bool    `json:"agreement"`
			} `json:"parent_local"`
		}
		if budget < 0 {
			if e := json.Unmarshal(out.Bytes(), &result); e != nil || code == 0 || calls != 9 || len(result.Parents) != 1 {
				t.Fatalf("partial code=%d calls=%d out=%s", code, calls, out.String())
			}
			continue
		}
		if e := json.Unmarshal(out.Bytes(), &result); e != nil || code != 0 || calls != 9 || result.Completed != 9 || len(result.Parents) != 3 {
			t.Fatalf("code=%d calls=%d out=%s stderr=%s", code, calls, out.String(), stderr.String())
		}
		if result.Parents[0].TotalVariation == nil || math.Abs(*result.Parents[0].TotalVariation-.1) > 1e-9 || result.Parents[0].Agreement == nil || !*result.Parents[0].Agreement || result.Parents[1].TotalVariation != nil || !result.Parents[2].Forced {
			t.Fatalf("parents=%+v", result.Parents)
		}
	}
}
