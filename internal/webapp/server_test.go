package webapp

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"jevcancode/internal/rank"
	"jevcancode/internal/runner"
	"jevcancode/internal/tokenspec"
	"jevcancode/internal/tokensynth"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestStartDuplicateCancelAndPersist(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "spec.json")
	data := `{"intent":"identity","inputs":["x"],"allowed_types":["int"],"public_tests":[{"inputs":{"x":1},"want":1}]}`
	if err := os.WriteFile(file, []byte(data), 0600); err != nil {
		t.Fatal(err)
	}
	started := make(chan struct{})
	s, err := New(Config{OutputDir: filepath.Join(dir, "output"), Problems: []Problem{{ID: "p", SpecPath: file}}, Models: []Model{{ID: "fake", NewRanker: func() rank.Ranker { return rank.Fake{} }}}, Run: func(ctx context.Context, _ tokenspec.Problem, _ rank.Ranker, q runner.Request) runner.Result {
		if q.Strategy != "beam" {
			t.Errorf("strategy %s", q.Strategy)
		}
		close(started)
		<-ctx.Done()
		return runner.Result{Status: "cancelled"}
	}})
	if err != nil {
		t.Fatal(err)
	}
	post := func(path, body string) *httptest.ResponseRecorder {
		r := httptest.NewRequest("POST", path, bytes.NewBufferString(body))
		r.Host = "127.0.0.1"
		r.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		s.ServeHTTP(w, r)
		return w
	}
	w := post("/api/runs", `{"model":"fake","problem":"p"}`)
	if w.Code != http.StatusAccepted {
		t.Fatalf("start %d %s", w.Code, w.Body.String())
	}
	var result struct {
		RunID string `json:"run_id"`
	}
	if err = json.Unmarshal(w.Body.Bytes(), &result); err != nil || result.RunID == "" {
		t.Fatal("missing run id")
	}
	<-started
	w = post("/api/runs", `{"model":"fake","problem":"p"}`)
	if w.Code != http.StatusConflict {
		t.Fatalf("duplicate %d", w.Code)
	}
	w = post("/api/runs/"+result.RunID+"/cancel", `{}`)
	if w.Code != http.StatusAccepted {
		t.Fatalf("cancel %d", w.Code)
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		w = httptest.NewRecorder()
		req := httptest.NewRequest("GET", "/api/history/"+result.RunID, nil)
		req.Host = "127.0.0.1"
		s.ServeHTTP(w, req)
		var snapshot Snapshot
		if w.Code == 200 && json.Unmarshal(w.Body.Bytes(), &snapshot) == nil && snapshot.Status == "cancelled" {
			trace := filepath.Join(dir, "output", result.RunID+".trace.jsonl")
			if _, err := os.Stat(trace); err != nil {
				t.Fatal(err)
			}
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal(fmt.Errorf("cancelled result not persisted"))
}
func TestJavaScriptModulesHaveJavaScriptContentType(t *testing.T) {
	s := &Server{}
	req := httptest.NewRequest("GET", "/app.mjs", nil)
	req.Host = "127.0.0.1"
	w := httptest.NewRecorder()
	s.ServeHTTP(w, req)
	if w.Code != 200 || !strings.HasPrefix(w.Header().Get("Content-Type"), "text/javascript") {
		t.Fatalf("status=%d content-type=%q", w.Code, w.Header().Get("Content-Type"))
	}
}
func TestFrontierResyncAndNonblockingSubscriber(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "spec.json")
	if err := os.WriteFile(file, []byte(`{"intent":"identity","inputs":["x"],"allowed_types":["int"],"public_tests":[{"inputs":{"x":1},"want":1}]}`), 0600); err != nil {
		t.Fatal(err)
	}
	ready := make(chan struct{})
	release := make(chan struct{})
	s, err := New(Config{OutputDir: filepath.Join(dir, "out"), Problems: []Problem{{ID: "p", SpecPath: file}}, Models: []Model{{ID: "fake", NewRanker: func() rank.Ranker { return rank.Fake{} }}}, Run: func(ctx context.Context, _ tokenspec.Problem, _ rank.Ranker, q runner.Request) runner.Result {
		if !q.TraceDecisionContext || q.TraceCode {
			t.Errorf("web trace must opt in to decision context without per-event source")
		}
		q.TokenTrace(tokensynth.Event{ID: "1", ParentID: "root", Lexeme: "package", Reason: "forced", Step: 1})
		q.TokenTrace(tokensynth.Event{ID: "2", ParentID: "1", Lexeme: "main", Reason: "available", Step: 2})
		q.TokenTrace(tokensynth.Event{ID: "model", Reason: "decision_context", DecisionContext: map[string]any{"intent": "identity", "parents": map[string]any{"1": map[string]any{"prefix": "package"}}}, Step: 2})
		score := 0.75
		q.TokenTrace(tokensynth.Event{ID: "2", ParentID: "1", Lexeme: "main", Reason: "ranked", Step: 2, Rank: 1, Score: &score})
		q.TokenTrace(tokensynth.Event{ID: "2", ParentID: "1", Lexeme: "main", Reason: "active", Step: 2})
		q.TokenTrace(tokensynth.Event{ID: "bad", ParentID: "1", Lexeme: ";", Reason: "body_rejected", RejectionReason: "unused_expression", Step: 2})
		q.TokenTrace(tokensynth.Event{ID: "frontier", Reason: "frontier", Step: 2, Frontier: []tokensynth.Path{{ID: "2", ParentID: "1", Rank: 1, TokenCount: 2}}, Calls: 1, Candidates: 2})
		q.TokenTrace(tokensynth.Event{ID: "2", Reason: "phase_started", Phase: "compile", Status: "started"})
		close(ready)
		<-release
		return runner.Result{Status: "success"}
	}})
	if err != nil {
		t.Fatal(err)
	}
	id, err := s.start("fake", "p")
	if err != nil {
		t.Fatal(err)
	}
	<-ready
	snap, err := s.snapshot(id)
	if err != nil || len(snap.Frontier) != 1 || snap.Frontier[0].Code != "package main" || len(snap.Candidates) != 1 || snap.Candidates[0].Code != "package main" || snap.Phase != "compile" {
		t.Fatalf("%+v %v", snap, err)
	}
	if snap.Candidates[0].Score == nil || *snap.Candidates[0].Score != 0.75 || snap.Candidates[0].Rank != 1 {
		t.Fatalf("rank lost: %+v", snap.Candidates[0])
	}
	data, _ := json.Marshal(snap)
	if !bytes.Contains(data, []byte(`"decision_context":{"intent":"identity"`)) {
		t.Fatalf("missing model context: %s", data)
	}
	if !bytes.Contains(data, []byte(`"body_rejection_reason":"unused_expression"`)) {
		t.Fatalf("missing body rejection: %s", data)
	}
	s.searchEvent(s.active, tokensynth.Event{ID: "3", ParentID: "2", Lexeme: ";", Reason: "available", Step: 3})
	snap, err = s.snapshot(id)
	data, _ = json.Marshal(snap)
	if bytes.Contains(data, []byte(`"decision_context"`)) {
		t.Fatalf("stale context survived next decision: %s", data)
	}
	s.mu.Lock()
	r := s.active
	slow := make(chan Envelope, 1)
	r.subscribers[slow] = struct{}{}
	start := time.Now()
	for i := 0; i < 100; i++ {
		s.emitLocked(r, "search", map[string]int{"i": i})
	}
	_, present := r.subscribers[slow]
	s.mu.Unlock()
	if present || time.Since(start) > 2*time.Second {
		t.Fatal("slow observer blocked generation")
	}
	close(release)
	select {
	case <-r.done:
	case <-time.After(5 * time.Second):
		t.Fatal("run did not finish")
	}
}
func TestStorageFailureIsTerminalAndVisible(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "spec.json")
	_ = os.WriteFile(file, []byte(`{"intent":"x","inputs":[],"allowed_types":["int"],"public_tests":[{"inputs":{},"want":1}]}`), 0600)
	release := make(chan struct{})
	s, err := New(Config{OutputDir: filepath.Join(dir, "out"), Problems: []Problem{{ID: "p", SpecPath: file}}, Models: []Model{{ID: "fake", NewRanker: func() rank.Ranker { return rank.Fake{} }}}, Run: func(context.Context, tokenspec.Problem, rank.Ranker, runner.Request) runner.Result {
		<-release
		return runner.Result{Status: "success"}
	}})
	if err != nil {
		t.Fatal(err)
	}
	id, err := s.start("fake", "p")
	if err != nil {
		t.Fatal(err)
	}
	s.mu.Lock()
	r := s.active
	ch := make(chan Envelope, 64)
	r.subscribers[ch] = struct{}{}
	s.mu.Unlock()
	if err = os.Remove(r.path); err != nil {
		t.Fatal(err)
	}
	if err = os.Mkdir(r.path, 0700); err != nil {
		t.Fatal(err)
	}
	close(release)
	select {
	case <-r.done:
	case <-time.After(5 * time.Second):
		t.Fatal("run did not finish")
	}
	var last Envelope
	for event := range ch {
		last = event
	}
	if last.Kind != "storage_error" {
		t.Fatalf("terminal %+v", last)
	}
	snap, err := s.snapshot(id)
	if err != nil || snap.Status != "storage_error" {
		t.Fatalf("snapshot=%+v err=%v", snap, err)
	}
}
func TestAllFortyMatchesRunnerFake(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "spec.json")
	data := `{"intent":"identity","inputs":["x"],"allowed_types":["int"],"public_tests":[{"inputs":{"x":1},"want":1}],"budgets":{"max_candidates":1}}`
	if err := os.WriteFile(file, []byte(data), 0600); err != nil {
		t.Fatal(err)
	}
	f, err := os.Open(file)
	if err != nil {
		t.Fatal(err)
	}
	p, err := tokenspec.Decode(f)
	f.Close()
	if err != nil {
		t.Fatal(err)
	}
	direct := runner.RunToken(context.Background(), p, rank.Fake{}, runner.Request{Strategy: "beam"})
	problems := make([]Problem, 40)
	for i := range problems {
		problems[i] = Problem{ID: fmt.Sprintf("p%02d", i), SpecPath: file}
	}
	s, err := New(Config{OutputDir: filepath.Join(dir, "out"), Problems: problems, Models: []Model{{ID: "fake", NewRanker: func() rank.Ranker { return rank.Fake{} }}}})
	if err != nil {
		t.Fatal(err)
	}
	id, err := s.start("fake", "all")
	if err != nil {
		t.Fatal(err)
	}
	s.mu.Lock()
	r := s.active
	s.mu.Unlock()
	select {
	case <-r.done:
	case <-time.After(40 * time.Second):
		t.Fatal("batch timed out")
	}
	snap, err := s.snapshot(id)
	if err != nil || snap.Status != "completed" || snap.Total != 40 || snap.Completed != 40 || len(snap.Results) != 40 {
		t.Fatalf("batch %d/%d %s %v", snap.Completed, snap.Total, snap.Status, err)
	}
	for _, item := range snap.Results {
		got := item.Result
		if got.Status != direct.Status || got.Calls != direct.Calls || got.Candidates != direct.Candidates || got.Source != direct.Source {
			t.Fatalf("parity %+v vs %+v", got, direct)
		}
	}
}
func TestEventsSnapshotWatermarkAndReplay(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "spec.json")
	_ = os.WriteFile(file, []byte(`{"intent":"x","inputs":[],"allowed_types":["int"],"public_tests":[{"inputs":{},"want":1}]}`), 0600)
	ready, release := make(chan struct{}), make(chan struct{})
	s, err := New(Config{OutputDir: filepath.Join(dir, "out"), Problems: []Problem{{ID: "p", SpecPath: file}}, Models: []Model{{ID: "fake", NewRanker: func() rank.Ranker { return rank.Fake{} }}}, Run: func(ctx context.Context, _ tokenspec.Problem, _ rank.Ranker, q runner.Request) runner.Result {
		q.TokenTrace(tokensynth.Event{ID: "1", ParentID: "root", Lexeme: "package", Reason: "forced", Step: 1})
		close(ready)
		<-release
		q.TokenTrace(tokensynth.Event{ID: "2", ParentID: "1", Lexeme: "main", Reason: "forced", Step: 1})
		return runner.Result{Status: "success"}
	}})
	if err != nil {
		t.Fatal(err)
	}
	id, err := s.start("fake", "p")
	if err != nil {
		t.Fatal(err)
	}
	<-ready
	server := httptest.NewServer(s)
	defer server.Close()
	resp, err := http.Get(server.URL + "/api/runs/" + id + "/events")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	reader := bufio.NewReader(resp.Body)
	readFrame := func() (string, error) {
		var b strings.Builder
		for {
			line, e := reader.ReadString('\n')
			if e != nil {
				return "", e
			}
			if line == "\n" {
				return b.String(), nil
			}
			b.WriteString(line)
		}
	}
	frame, err := readFrame()
	if err != nil || !strings.Contains(frame, "event: snapshot") {
		t.Fatalf("frame %s %v", frame, err)
	}
	var snap Snapshot
	for _, line := range strings.Split(frame, "\n") {
		if strings.HasPrefix(line, "data: ") {
			if err = json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &snap); err != nil {
				t.Fatal(err)
			}
		}
	}
	traceResp, err := http.Get(server.URL + "/api/runs/" + id + "/trace?problem=p")
	if err != nil {
		t.Fatal(err)
	}
	traceBody, _ := io.ReadAll(traceResp.Body)
	traceResp.Body.Close()
	if !bytes.Contains(traceBody, []byte(`"lexeme":"package"`)) {
		t.Fatal("active journal missing event")
	}
	close(release)
	first, err := readFrame()
	if err != nil {
		t.Fatal(err)
	}
	var event Envelope
	for _, line := range strings.Split(first, "\n") {
		if strings.HasPrefix(line, "data: ") {
			if err = json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &event); err != nil {
				t.Fatal(err)
			}
		}
	}
	if event.Seq != snap.Seq+1 {
		t.Fatalf("snapshot=%d event=%d", snap.Seq, event.Seq)
	}
}
func TestCatalogBudgetsAndFrontierClear(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "spec.json")
	data := `{"intent":"x","inputs":[],"allowed_types":["int"],"public_tests":[{"inputs":{},"want":1}],"budgets":{"max_calls":7,"max_candidates":9,"max_tokens":11,"timeout_seconds":13}}`
	if err := os.WriteFile(file, []byte(data), 0600); err != nil {
		t.Fatal(err)
	}
	ready := make(chan struct{})
	release := make(chan struct{})
	s, err := New(Config{OutputDir: filepath.Join(dir, "out"), Problems: []Problem{{ID: "p", SpecPath: file}}, Models: []Model{{ID: "fake", NewRanker: func() rank.Ranker { return rank.Fake{} }}}, Run: func(_ context.Context, _ tokenspec.Problem, _ rank.Ranker, q runner.Request) runner.Result {
		q.TokenTrace(tokensynth.Event{ID: "frontier", Reason: "frontier", Frontier: []tokensynth.Path{{ID: "root", Rank: 1}}})
		q.TokenTrace(tokensynth.Event{ID: "frontier", Reason: "frontier"})
		close(ready)
		<-release
		return runner.Result{Status: "success"}
	}})
	if err != nil {
		t.Fatal(err)
	}
	id, err := s.start("fake", "p")
	if err != nil {
		t.Fatal(err)
	}
	// 2026-11-07: 임시 디렉터리 정리 전에 비동기 기록 저장이 끝나야 한다.
	t.Cleanup(func() {
		close(release)
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := s.Close(ctx); err != nil {
			t.Errorf("server shutdown: %v", err)
		}
	})
	<-ready
	snap, err := s.snapshot(id)
	if err != nil || len(snap.Frontier) != 0 || snap.Current == nil || snap.Current.Budgets.MaxCalls != 7 || snap.Metrics["max_calls"] != float64(7) || snap.Current.Deadline == nil {
		t.Fatalf("%+v %v", snap, err)
	}
}
func TestRejectForeignOriginAndWrongContentType(t *testing.T) {
	s := &Server{}
	r := httptest.NewRequest("GET", "/api/catalog", nil)
	r.Host = "127.0.0.1:8080"
	r.Header.Set("Origin", "http://evil.invalid")
	w := httptest.NewRecorder()
	s.ServeHTTP(w, r)
	if w.Code != 403 {
		t.Fatalf("origin %d", w.Code)
	}
	r = httptest.NewRequest("POST", "/api/runs", strings.NewReader(`{}`))
	r.Host = "127.0.0.1:8080"
	r.Header.Set("Content-Type", "text/plain")
	w = httptest.NewRecorder()
	s.ServeHTTP(w, r)
	if w.Code != 415 {
		t.Fatalf("content type %d", w.Code)
	}
}
func TestExistingTraceIsNeverOverwritten(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "spec.json")
	_ = os.WriteFile(file, []byte(`{"intent":"x","inputs":[],"allowed_types":["int"],"public_tests":[{"inputs":{},"want":1}]}`), 0600)
	release := make(chan struct{})
	s, err := New(Config{OutputDir: filepath.Join(dir, "out"), Problems: []Problem{{ID: "p", SpecPath: file}}, Models: []Model{{ID: "fake", NewRanker: func() rank.Ranker { return rank.Fake{} }}}, Run: func(context.Context, tokenspec.Problem, rank.Ranker, runner.Request) runner.Result {
		<-release
		return runner.Result{Status: "success"}
	}})
	if err != nil {
		t.Fatal(err)
	}
	id, err := s.start("fake", "p")
	if err != nil {
		t.Fatal(err)
	}
	s.mu.Lock()
	r := s.active
	s.mu.Unlock()
	if err = os.WriteFile(r.finalTrace, []byte("existing"), 0600); err != nil {
		t.Fatal(err)
	}
	close(release)
	select {
	case <-r.done:
	case <-time.After(5 * time.Second):
		t.Fatal("run did not finish")
	}
	b, err := os.ReadFile(r.finalTrace)
	if err != nil || string(b) != "existing" {
		t.Fatalf("trace overwritten: %q %v", b, err)
	}
	snap, err := s.snapshot(id)
	if err != nil || snap.Status != "storage_error" {
		t.Fatalf("snapshot %+v %v", snap, err)
	}
}
func TestCloseCancelsAndPersists(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "spec.json")
	_ = os.WriteFile(file, []byte(`{"intent":"x","inputs":[],"allowed_types":["int"],"public_tests":[{"inputs":{},"want":1}]}`), 0600)
	started := make(chan struct{})
	s, err := New(Config{OutputDir: filepath.Join(dir, "out"), Problems: []Problem{{ID: "p", SpecPath: file}}, Models: []Model{{ID: "fake", NewRanker: func() rank.Ranker { return rank.Fake{} }}}, Run: func(ctx context.Context, _ tokenspec.Problem, _ rank.Ranker, _ runner.Request) runner.Result {
		close(started)
		<-ctx.Done()
		return runner.Result{Status: "cancelled"}
	}})
	if err != nil {
		t.Fatal(err)
	}
	id, err := s.start("fake", "p")
	if err != nil {
		t.Fatal(err)
	}
	<-started
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err = s.Close(ctx); err != nil {
		t.Fatal(err)
	}
	snap, err := s.snapshot(id)
	if err != nil || snap.Status != "cancelled" {
		t.Fatalf("%+v %v", snap, err)
	}
}
func TestTraceDownloadHeader(t *testing.T) {
	dir := t.TempDir()
	id := "2026-05-11.001"
	content := []byte("{\"seq\":1}\n")
	if err := os.WriteFile(filepath.Join(dir, id+".trace.jsonl"), content, 0600); err != nil {
		t.Fatal(err)
	}
	s := &Server{cfg: Config{OutputDir: dir}}
	req := httptest.NewRequest("GET", "/api/history/"+id+"/trace", nil)
	req.Host = "127.0.0.1"
	w := httptest.NewRecorder()
	s.ServeHTTP(w, req)
	want := `attachment; filename="` + id + `.trace.jsonl"`
	if w.Code != 200 || w.Header().Get("Content-Disposition") != want || !bytes.Equal(w.Body.Bytes(), content) {
		t.Fatalf("status=%d disposition=%q body=%q", w.Code, w.Header().Get("Content-Disposition"), w.Body.String())
	}
}
func TestTraceProblemFilter(t *testing.T) {
	dir := t.TempDir()
	id := "2026-05-11.001"
	trace := strings.Join([]string{`{"seq":1,"kind":"run_started","data":{}}`, `{"seq":2,"kind":"problem_started","data":{"id":"first"}}`, `{"seq":3,"kind":"search","data":{"lexeme":"first-only"}}`, `{"seq":4,"kind":"problem_finished","data":{"problem_id":"first"}}`, `{"seq":5,"kind":"problem_started","data":{"id":"other"}}`, `{"seq":6,"kind":"search","data":{"lexeme":"other-only"}}`, `{"seq":7,"kind":"problem_finished","data":{"problem_id":"other"}}`, `{"seq":8,"kind":"problem_skipped","data":{"problem_id":"skipped"}}`, `{"seq":9,"kind":"run_finished","data":{}}`}, "\n") + "\n"
	if err := os.WriteFile(filepath.Join(dir, id+".trace.jsonl"), []byte(trace), 0600); err != nil {
		t.Fatal(err)
	}
	s := &Server{cfg: Config{OutputDir: dir}, problems: map[string]loadedProblem{"first": {}, "other": {}, "skipped": {}}}
	get := func(query string) *httptest.ResponseRecorder {
		req := httptest.NewRequest("GET", "/api/history/"+id+"/trace"+query, nil)
		req.Host = "127.0.0.1"
		w := httptest.NewRecorder()
		s.ServeHTTP(w, req)
		return w
	}
	w := get("?problem=first")
	if w.Code != 200 || !strings.Contains(w.Body.String(), "first-only") || strings.Contains(w.Body.String(), "other-only") || strings.Contains(w.Body.String(), "run_finished") {
		t.Fatalf("first: %d %s", w.Code, w.Body.String())
	}
	w = get("?problem=other")
	if w.Code != 200 || !strings.Contains(w.Body.String(), "other-only") || strings.Contains(w.Body.String(), "first-only") || strings.Contains(w.Body.String(), "skipped") || strings.Contains(w.Body.String(), "run_finished") {
		t.Fatalf("other: %d %s", w.Code, w.Body.String())
	}
	w = get("?problem=skipped")
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"problem_id":"skipped"`) || strings.Contains(w.Body.String(), "other-only") {
		t.Fatalf("skipped: %d %s", w.Code, w.Body.String())
	}
	w = get("?problem=unknown")
	if w.Code != 400 {
		t.Fatalf("unknown status %d", w.Code)
	}
	for _, q := range []string{"?problem=", "?problem=first&problem=other"} {
		if got := get(q); got.Code != 400 {
			t.Fatalf("%s status %d", q, got.Code)
		}
	}
	w = get("")
	if w.Code != 200 || w.Body.String() != trace {
		t.Fatal("unfiltered trace changed")
	}
}
