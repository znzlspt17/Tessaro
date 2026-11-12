package application

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"jevcancode/internal/compare"
	"jevcancode/internal/rank"
	"jevcancode/internal/runner"
	"jevcancode/internal/tokensynth"
)

type blockingRanker struct{ entered chan struct{} }

func (r *blockingRanker) Rank(ctx context.Context, _ string, _ []rank.Candidate) ([]rank.Score, error) {
	select {
	case r.entered <- struct{}{}:
	default:
	}
	<-ctx.Done()
	return nil, ctx.Err()
}

func TestPreparationPhaseIsObservable(t *testing.T) {
	entered := make(chan struct{})
	release := make(chan struct{})
	m := New(Config{OutputDir: t.TempDir(), Resolve: func(context.Context, compare.Model) (rank.Ranker, error) {
		close(entered)
		<-release
		return nil, errors.New("worker_initialization_failed")
	}})
	_, e := m.Start(Request{Kind: "search", Engine: "token", SpecPath: testSpec(t), ProblemID: "p", Model: compare.Model{ID: "x", Ranker: "local"}})
	if e != nil {
		t.Fatal(e)
	}
	<-entered
	b, _ := json.Marshal(m.Snapshot())
	var got map[string]any
	_ = json.Unmarshal(b, &got)
	if got["phase"] != "model_preparing" {
		close(release)
		t.Fatalf("preparing snapshot=%s", b)
	}
	close(release)
	s := waitEnd(t, m)
	b, _ = json.Marshal(s)
	_ = json.Unmarshal(b, &got)
	if got["phase"] != "model_error" {
		t.Fatalf("failed snapshot=%s", b)
	}
}

func TestRejectionReasonsAreVisibleAndResetByProblemStart(t *testing.T) {
	m := New(Config{OutputDir: t.TempDir()})
	f, e := os.CreateTemp(t.TempDir(), "journal")
	if e != nil {
		t.Fatal(e)
	}
	defer f.Close()
	m.journal = f
	m.nodes = map[string]node{}
	m.tokenEvent(tokensynth.Event{Reason: "declaration_rejected", RejectionReason: "invalid_type"})
	m.tokenEvent(tokensynth.Event{Reason: "body_rejected", RejectionReason: "unused_expression"})
	b, _ := json.Marshal(m.Snapshot())
	var got map[string]any
	_ = json.Unmarshal(b, &got)
	if got["declaration_rejection_reason"] != "invalid_type" || got["body_rejection_reason"] != "unused_expression" {
		t.Fatalf("snapshot=%s", b)
	}
	m.problemStart(PublicProblem{ID: "next"})
	b, _ = json.Marshal(m.Snapshot())
	got = map[string]any{}
	_ = json.Unmarshal(b, &got)
	if got["declaration_rejection_reason"] != nil || got["body_rejection_reason"] != nil {
		t.Fatalf("stale rejection=%s", b)
	}
}

func TestPreRankRejectionsAppearWithTheirReasonAndStep(t *testing.T) {
	m := New(Config{OutputDir: t.TempDir()})
	f, err := os.CreateTemp(t.TempDir(), "journal")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	m.journal = f
	m.nodes = map[string]node{"p": {parent: "root", lexeme: "n"}}
	m.tokenEvent(tokensynth.Event{ID: "bad", ParentID: "p", Lexeme: "(", TokenKind: "(", Reason: "body_rejected", RejectionReason: "non_callable", Step: 1})
	m.tokenEvent(tokensynth.Event{ID: "ok", ParentID: "p", Lexeme: "+", TokenKind: "+", Reason: "available", Step: 1})
	b, _ := json.Marshal(m.Snapshot())
	var result struct {
		Candidates []map[string]any `json:"candidates"`
	}
	_ = json.Unmarshal(b, &result)
	if len(result.Candidates) != 2 || result.Candidates[0]["id"] != "bad" || result.Candidates[0]["rejection_reason"] != "non_callable" || result.Candidates[0]["reason"] != "body_rejected" || result.Candidates[0]["code"] != "n (" {
		t.Fatalf("missing rejected candidate or reason: %s", b)
	}
	m.tokenEvent(tokensynth.Event{ID: "bad2", ParentID: "p", Lexeme: "(", TokenKind: "(", Reason: "declaration_rejected", RejectionReason: "invalid_signature", Step: 2})
	b, _ = json.Marshal(m.Snapshot())
	_ = json.Unmarshal(b, &result)
	if len(result.Candidates) != 1 || result.Candidates[0]["id"] != "bad2" || result.Candidates[0]["rejection_reason"] != "invalid_signature" {
		t.Fatalf("new step did not replace previous candidates: %s", b)
	}
	m.tokenEvent(tokensynth.Event{ID: "sent", ParentID: "p", Lexeme: "+", TokenKind: "+", Reason: "available", Step: 2})
	m.tokenEvent(tokensynth.Event{ID: "sent", ParentID: "p", Lexeme: "+", TokenKind: "+", Reason: "declaration_rejected", RejectionReason: "invalid_signature", Step: 2})
	b, _ = json.Marshal(m.Snapshot())
	_ = json.Unmarshal(b, &result)
	if len(result.Candidates) != 2 || result.Candidates[1]["rejection_reason"] != "invalid_signature" {
		t.Fatalf("existing candidate rejection should update the same row: %s", b)
	}
}

func TestRejectedCandidatePreservesZeroTokenIndex(t *testing.T) {
	m := New(Config{OutputDir: t.TempDir()})
	f, err := os.CreateTemp(t.TempDir(), "journal")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	m.journal = f
	m.nodes = map[string]node{}
	zero := 0
	m.tokenEvent(tokensynth.Event{ID: "r", ParentID: "p", Lexeme: "<", Reason: "body_rejected", RejectionReason: "bool_order_comparison", RejectionTokenIndex: &zero, Step: 1})
	b, err := json.Marshal(m.Snapshot())
	if err != nil {
		t.Fatal(err)
	}
	var got struct {
		Candidates []map[string]any `json:"candidates"`
	}
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatal(err)
	}
	if len(got.Candidates) != 1 || got.Candidates[0]["rejection_token_index"] != float64(0) {
		t.Fatalf("missing zero index: %s", b)
	}
}
func waitEnd(t *testing.T, m *Manager) Snapshot {
	t.Helper()
	deadline := time.After(15 * time.Second)
	for {
		v := m.Snapshot()
		if v.Status != "running" && v.Status != "cancelling" {
			return v
		}
		select {
		case <-m.Changed():
		case <-deadline:
			t.Fatal("job did not end")
		}
	}
}
func testSpec(t *testing.T) string {
	t.Helper()
	p, e := filepath.Abs("../../examples/token/specs/loop-factorial.json")
	if e != nil {
		t.Fatal(e)
	}
	return p
}

func TestManagerPreventsConcurrentJobsAndCancels(t *testing.T) {
	r := &blockingRanker{entered: make(chan struct{}, 1)}
	m := New(Config{OutputDir: t.TempDir(), Resolve: func(context.Context, compare.Model) (rank.Ranker, error) { return r, nil }})
	q := Request{Kind: "search", Engine: "token", SpecPath: testSpec(t), ProblemID: "factorial", Model: compare.Model{ID: "x", Ranker: "local"}, Options: runner.Request{MaxCalls: 3}}
	id, e := m.Start(q)
	if e != nil {
		t.Fatal(e)
	}
	select {
	case <-r.entered:
	case <-time.After(10 * time.Second):
		t.Fatal("model never called")
	}
	if _, e = m.Start(q); e == nil || e.Error() != "busy" {
		t.Fatalf("expected busy, got %v", e)
	}
	if e = m.Cancel(); e != nil {
		t.Fatal(e)
	}
	s := waitEnd(t, m)
	if s.Status != "cancelled" || s.RunID != id || len(s.Results) != 1 {
		t.Fatalf("snapshot %+v", s)
	}
	record, e := m.ReadHistory(id)
	if e != nil || record.Kind != "desktop_run" || !record.Trace {
		t.Fatalf("record %+v: %v", record, e)
	}
	events, e := m.ReadTrace(id)
	if e != nil || len(events) < 3 {
		t.Fatalf("trace len=%d: %v", len(events), e)
	}
	for i, event := range events {
		if event.Seq != uint64(i+1) {
			t.Fatalf("sequence %d: %+v", i, event)
		}
	}
}
func TestRecordCompatibilityAndPartialTrace(t *testing.T) {
	dir := t.TempDir()
	m := New(Config{OutputDir: dir})
	id := "2026-11-07.001"
	old := `{"kind":"web_run","run_id":"2026-11-07.001","status":"running"}`
	if e := os.WriteFile(filepath.Join(dir, id), []byte(old), 0600); e != nil {
		t.Fatal(e)
	}
	trace := "{\"seq\":1,\"run_id\":\"" + id + "\",\"kind\":\"search\",\"data\":{}}\n{\"seq\":2"
	if e := os.WriteFile(filepath.Join(dir, id+".trace.tmp"), []byte(trace), 0600); e != nil {
		t.Fatal(e)
	}
	record, e := m.ReadHistory(id)
	if e != nil || record.Status != "interrupted" || string(record.Data) != old {
		t.Fatalf("record %+v %v", record, e)
	}
	events, e := m.ReadTrace(id)
	if e != nil || len(events) != 1 || events[0].Seq != 1 {
		t.Fatalf("events %+v %v", events, e)
	}
	if e = os.WriteFile(filepath.Join(dir, id+".trace.tmp"), []byte("{bad}\n{}\n"), 0600); e != nil {
		t.Fatal(e)
	}
	if _, e = m.ReadTrace(id); e == nil || !strings.Contains(e.Error(), "line 1") {
		t.Fatalf("middle corruption: %v", e)
	}
	if _, e = m.ReadHistory("../outside"); !errors.Is(e, os.ErrNotExist) {
		t.Fatalf("path error: %v", e)
	}
}
func TestReportClassification(t *testing.T) {
	dir := t.TempDir()
	m := New(Config{OutputDir: dir})
	for id, content := range map[string]string{"2026-11-07.001": `{"groups":[],"outcomes":[]}`, "2026-11-07.002": `{"evaluation_kind":"complete_candidate_selection","items":[]}`, "2026-11-07.003": `{"engine":"token","candidate_unit":"go_lexical_token"}`} {
		if e := os.WriteFile(filepath.Join(dir, id), []byte(content), 0600); e != nil {
			t.Fatal(e)
		}
	}
	got, e := m.History()
	if e != nil || len(got) != 3 {
		t.Fatalf("records %+v: %v", got, e)
	}
	seen := map[string]bool{}
	for _, record := range got {
		seen[record.Kind] = true
		if !json.Valid(record.Data) {
			t.Fatal("invalid JSON")
		}
	}
	for _, kind := range []string{"compare", "benchmark", "synthesis"} {
		if !seen[kind] {
			t.Errorf("missing %s", kind)
		}
	}
}

func TestTerminalJournalFailureIsReportedAndVisibleInHistory(t *testing.T) {
	var m *Manager
	var closeErr error
	entry := make(chan struct{})
	proceed := make(chan struct{})
	m = New(Config{OutputDir: t.TempDir(), Resolve: func(context.Context, compare.Model) (rank.Ranker, error) {
		close(entry)
		<-proceed
		m.mu.Lock()
		closeErr = m.journal.Close()
		m.mu.Unlock()
		return nil, errors.New("worker_initialization_failed")
	}})
	q := Request{Kind: "search", Engine: "token", SpecPath: testSpec(t), ProblemID: "p", Model: compare.Model{ID: "x", Ranker: "local"}}
	id, e := m.Start(q)
	if e != nil {
		t.Fatal(e)
	}
	<-entry
	close(proceed)
	s := waitEnd(t, m)
	if closeErr != nil || s.Status != "storage_error" {
		t.Fatalf("close=%v, snapshot=%+v", closeErr, s)
	}
	record, e := m.ReadHistory(id)
	if e != nil || record.Status != "storage_error" {
		t.Fatalf("history=%+v, error=%v", record, e)
	}
	events, e := m.ReadTrace(id)
	if e != nil {
		t.Fatal(e)
	}
	if len(events) == 0 || events[len(events)-1].Kind != "storage_error" {
		t.Fatalf("missing terminal storage error: %+v", events)
	}
}

func TestPartialJournalWriteRollsBackBeforeTerminal(t *testing.T) {
	var m *Manager
	m = New(Config{OutputDir: t.TempDir(), Resolve: func(context.Context, compare.Model) (rank.Ranker, error) {
		m.mu.Lock()
		_, _ = m.journal.Write([]byte(`{"incomplete"`))
		_ = m.journal.Close()
		m.mu.Unlock()
		return nil, errors.New("worker_initialization_failed")
	}})
	id, e := m.Start(Request{Kind: "search", Engine: "token", SpecPath: testSpec(t), ProblemID: "p", Model: compare.Model{ID: "x", Ranker: "local"}})
	if e != nil {
		t.Fatal(e)
	}
	s := waitEnd(t, m)
	if s.Status != "storage_error" {
		t.Fatalf("status=%s", s.Status)
	}
	events, e := m.ReadTrace(id)
	if e != nil || len(events) == 0 || events[len(events)-1].Kind != "storage_error" {
		t.Fatalf("events=%+v err=%v", events, e)
	}
}
func TestWorkerInitializationFailureHasSafeCategory(t *testing.T) {
	if got := safeError(errors.New("worker_initialization_failed")); got != "worker_initialization_failed" {
		t.Fatalf("category=%q", got)
	}
}

func TestOversizedSnapshotRejectedWithoutReplacingReadableState(t *testing.T) {
	path := filepath.Join(t.TempDir(), "record")
	if e := os.WriteFile(path, []byte(`{"status":"running"}`), 0600); e != nil {
		t.Fatal(e)
	}
	s := Snapshot{Payload: json.RawMessage(`"` + strings.Repeat("a", 33<<20) + `"`)}
	if e := saveSnapshot(path, s); e == nil {
		t.Fatal("expected oversized snapshot rejection")
	}
	b, e := os.ReadFile(path)
	if e != nil || string(b) != `{"status":"running"}` {
		t.Fatalf("previous record altered: %v", e)
	}
}

func TestBenchmarkAppliesTotalTimeoutAfterModelPreparation(t *testing.T) {
	var entered atomic.Int32
	r := &timeoutRanker{entered: &entered}
	m := New(Config{OutputDir: t.TempDir(), Resolve: func(ctx context.Context, _ compare.Model) (rank.Ranker, error) {
		time.Sleep(30 * time.Millisecond)
		return r, nil
	}})
	p, e := filepath.Abs("../../examples/benchmark.json")
	if e != nil {
		t.Fatal(e)
	}
	_, e = m.Start(Request{Kind: "benchmark", ManifestPath: p, Model: compare.Model{ID: "x", Ranker: "local"}, Options: runner.Request{Timeout: 20 * time.Millisecond}})
	if e != nil {
		t.Fatal(e)
	}
	s := waitEnd(t, m)
	if s.Status != "timeout" || entered.Load() != 1 || len(s.Payload) == 0 {
		t.Fatalf("snapshot status=%s calls=%d payload=%t", s.Status, entered.Load(), len(s.Payload) > 0)
	}
}

func TestLargeReportRecordAndSummaryTraceRemainReadable(t *testing.T) {
	dir := t.TempDir()
	m := New(Config{OutputDir: dir})
	id := "2026-11-07.001"
	payload, _ := json.Marshal(map[string]string{"large_report": strings.Repeat("a", 9<<20)})
	s := Snapshot{Kind: "desktop_run", SchemaVersion: 1, RunID: id, Status: "completed", Payload: payload}
	if e := saveSnapshot(filepath.Join(dir, id), s); e != nil {
		t.Fatal(e)
	}
	trace := `{"seq":1,"run_id":"2026-11-07.001","kind":"report","data":{"job_kind":"compare","total":1}}` + "\n" + `{"seq":2,"run_id":"2026-11-07.001","kind":"run_finished","data":{"status":"completed"}}` + "\n"
	if e := os.WriteFile(filepath.Join(dir, id+".trace.jsonl"), []byte(trace), 0600); e != nil {
		t.Fatal(e)
	}
	r, e := m.ReadHistory(id)
	if e != nil || r.Status != "completed" {
		t.Fatalf("history status=%q error=%v", r.Status, e)
	}
	var restored Snapshot
	if e = json.Unmarshal(r.Data, &restored); e != nil {
		t.Fatal(e)
	}
	var decoded map[string]string
	if e = json.Unmarshal(restored.Payload, &decoded); e != nil || len(decoded["large_report"]) != 9<<20 {
		t.Fatalf("report size=%d error=%v", len(decoded["large_report"]), e)
	}
	events, e := m.ReadTrace(id)
	if e != nil || len(events) != 2 {
		t.Fatalf("trace len=%d error=%v", len(events), e)
	}
}

type timeoutRanker struct{ entered *atomic.Int32 }

func (r *timeoutRanker) Rank(ctx context.Context, _ string, _ []rank.Candidate) ([]rank.Score, error) {
	r.entered.Add(1)
	<-ctx.Done()
	return nil, ctx.Err()
}
