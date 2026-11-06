package webapp

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"jevcancoding/internal/app"
	"jevcancoding/internal/rank"
	"jevcancoding/internal/runner"
	"jevcancoding/internal/tokenspec"
	"jevcancoding/internal/tokensynth"
	"os"
	"path/filepath"
	"sync"
	"time"
)

type Problem struct{ ID, Label, SpecPath, HeldoutPath string }
type Model struct {
	ID, Label string
	NewRanker func() rank.Ranker
}
type Config struct {
	OutputDir string
	Problems  []Problem
	Models    []Model
	Run       func(context.Context, tokenspec.Problem, rank.Ranker, runner.Request) runner.Result
}
type PublicProblem struct {
	ID          string           `json:"id"`
	Label       string           `json:"label"`
	Intent      string           `json:"intent"`
	PublicTests []tokenspec.Test `json:"public_tests"`
	Budgets     PublicBudgets    `json:"budgets"`
	Deadline    *time.Time       `json:"deadline,omitempty"`
}
type PublicBudgets struct {
	MaxTokens      int `json:"max_tokens"`
	MaxCalls       int `json:"max_calls"`
	MaxCandidates  int `json:"max_candidates"`
	MaxLocals      int `json:"max_locals"`
	MaxStatements  int `json:"max_statements"`
	MaxNesting     int `json:"max_nesting"`
	TimeoutSeconds int `json:"timeout_seconds"`
}

func publicBudgets(b tokenspec.Budgets) PublicBudgets {
	out := PublicBudgets{256, 128, 8192, 8, 32, 4, 300}
	if b.MaxTokens > 0 {
		out.MaxTokens = b.MaxTokens
	}
	if b.MaxCalls > 0 {
		out.MaxCalls = b.MaxCalls
	}
	if b.MaxCandidates > 0 {
		out.MaxCandidates = b.MaxCandidates
	}
	if b.MaxLocals > 0 {
		out.MaxLocals = b.MaxLocals
	}
	if b.MaxStatements > 0 {
		out.MaxStatements = b.MaxStatements
	}
	if b.MaxNesting > 0 {
		out.MaxNesting = b.MaxNesting
	}
	if b.TimeoutSeconds > 0 {
		out.TimeoutSeconds = b.TimeoutSeconds
	}
	return out
}

type catalogModel struct {
	ID    string `json:"id"`
	Label string `json:"label"`
}
type FrontierItem struct {
	ID         string `json:"id"`
	ParentID   string `json:"parent_id,omitempty"`
	Rank       int    `json:"rank"`
	TokenCount int    `json:"token_count"`
	Code       string `json:"code"`
}
type CandidateItem struct {
	ID        string   `json:"id"`
	ParentID  string   `json:"parent_id,omitempty"`
	Rank      int      `json:"rank,omitempty"`
	Score     *float64 `json:"score,omitempty"`
	Lexeme    string   `json:"lexeme"`
	TokenKind string   `json:"token_kind,omitempty"`
	Reason    string   `json:"reason"`
	Code      string   `json:"code"`
}
type ProblemResult struct {
	ProblemID string        `json:"problem_id"`
	Result    runner.Result `json:"result"`
}
type Snapshot struct {
	Kind                  string          `json:"kind"`
	Seq                   uint64          `json:"seq"`
	RunID                 string          `json:"run_id"`
	Status                string          `json:"status"`
	Model                 string          `json:"model"`
	Problem               string          `json:"problem"`
	Total                 int             `json:"total"`
	Completed             int             `json:"completed"`
	Succeeded             int             `json:"succeeded"`
	Failed                int             `json:"failed"`
	NotRun                int             `json:"not_run"`
	Current               *PublicProblem  `json:"current"`
	Frontier              []FrontierItem  `json:"frontier"`
	Candidates            []CandidateItem `json:"candidates"`
	DecisionContext       map[string]any  `json:"decision_context,omitempty"`
	BodyRejectionReason   string          `json:"body_rejection_reason,omitempty"`
	Step                  int             `json:"step"`
	Phase                 string          `json:"phase,omitempty"`
	PhaseStatus           string          `json:"phase_status,omitempty"`
	ValidationCandidateID string          `json:"validation_candidate_id,omitempty"`
	ProblemDeadline       *time.Time      `json:"problem_deadline,omitempty"`
	ModelDeadline         *time.Time      `json:"model_deadline,omitempty"`
	Metrics               map[string]any  `json:"metrics"`
	Results               []ProblemResult `json:"results"`
	StartedAt             time.Time       `json:"started_at"`
	FinishedAt            *time.Time      `json:"finished_at,omitempty"`
}
type Envelope struct {
	Seq   uint64 `json:"seq"`
	RunID string `json:"run_id"`
	Kind  string `json:"kind"`
	Data  any    `json:"data"`
}
type loadedProblem struct {
	config Problem
	public PublicProblem
	spec   tokenspec.Problem
}
type node struct{ parent, lexeme string }
type runState struct {
	snapshot                   Snapshot
	path, tmpTrace, finalTrace string
	journal                    *os.File
	cancel                     context.CancelFunc
	subscribers                map[chan Envelope]struct{}
	nodes                      map[string]node
	storageFailed              bool
	candidateStep              int
	done                       chan struct{}
}
type Server struct {
	mu       sync.Mutex
	cfg      Config
	problems map[string]loadedProblem
	order    []string
	models   map[string]Model
	active   *runState
	failed   map[string]Snapshot
}

func New(cfg Config) (*Server, error) {
	if cfg.OutputDir == "" {
		cfg.OutputDir = "output"
	}
	if cfg.Run == nil {
		cfg.Run = runner.RunToken
	}
	s := &Server{cfg: cfg, problems: map[string]loadedProblem{}, models: map[string]Model{}, failed: map[string]Snapshot{}}
	for _, m := range cfg.Models {
		if m.ID == "" || m.NewRanker == nil || s.models[m.ID].ID != "" {
			return nil, fmt.Errorf("invalid model")
		}
		if m.Label == "" {
			m.Label = m.ID
		}
		s.models[m.ID] = m
	}
	for _, item := range cfg.Problems {
		if item.ID == "" || item.ID == "all" || s.problems[item.ID].config.ID != "" {
			return nil, fmt.Errorf("invalid problem")
		}
		f, e := os.Open(item.SpecPath)
		if e != nil {
			return nil, fmt.Errorf("problem load failed")
		}
		p, e := tokenspec.Decode(f)
		f.Close()
		if e != nil {
			return nil, fmt.Errorf("problem specification invalid")
		}
		if item.Label == "" {
			item.Label = item.ID
		}
		s.order = append(s.order, item.ID)
		s.problems[item.ID] = loadedProblem{config: item, spec: p, public: PublicProblem{ID: item.ID, Label: item.Label, Intent: p.Intent, PublicTests: p.PublicTests, Budgets: publicBudgets(p.Budgets)}}
	}
	if len(s.models) == 0 || len(s.order) == 0 {
		return nil, fmt.Errorf("models and problems required")
	}
	return s, nil
}
func (s *Server) start(modelID, problemID string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.active != nil {
		return "", errBusy
	}
	model, ok := s.models[modelID]
	if !ok {
		return "", errInvalid
	}
	ids := []string{problemID}
	if problemID == "all" {
		ids = append([]string(nil), s.order...)
	} else if _, ok = s.problems[problemID]; !ok {
		return "", errInvalid
	}
	snapshot := Snapshot{Kind: "web_run", Status: "running", Model: model.ID, Problem: problemID, Total: len(ids), Frontier: []FrontierItem{}, Candidates: []CandidateItem{}, Metrics: map[string]any{}, Results: []ProblemResult{}, StartedAt: time.Now().UTC()}
	path, e := app.WriteResultWithPath(discard{}, s.cfg.OutputDir, time.Now(), snapshot)
	if e != nil {
		return "", fmt.Errorf("storage unavailable")
	}
	id := filepath.Base(path)
	snapshot.RunID = id
	tmp := path + ".trace.tmp"
	journal, e := os.OpenFile(tmp, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if e != nil {
		_ = os.Remove(path)
		return "", fmt.Errorf("journal unavailable")
	}
	ctx, cancel := context.WithCancel(context.Background())
	state := &runState{snapshot: snapshot, path: path, tmpTrace: tmp, finalTrace: path + ".trace.jsonl", journal: journal, cancel: cancel, subscribers: map[chan Envelope]struct{}{}, nodes: map[string]node{}, done: make(chan struct{})}
	s.active = state
	s.emitLocked(state, "run_started", map[string]any{"total": len(ids)})
	go s.execute(ctx, state, model, ids)
	return id, nil
}

type discard struct{}

func (discard) Write(p []byte) (int, error) { return len(p), nil }

// 2026-11-05: 모델 실행과 네트워크 소비를 분리하고 journal을 순서대로 기록한다.
func (s *Server) emitLocked(r *runState, kind string, data any) {
	r.snapshot.Seq++
	event := Envelope{Seq: r.snapshot.Seq, RunID: r.snapshot.RunID, Kind: kind, Data: data}
	if e := record(r, event); e != nil {
		r.storageFailed = true
		r.cancel()
	}
	broadcast(r, event)
}
func record(r *runState, event Envelope) error {
	encoded, e := json.Marshal(event)
	if e == nil {
		encoded = append(encoded, '\n')
		var written int
		written, e = r.journal.Write(encoded)
		if e == nil && written != len(encoded) {
			e = io.ErrShortWrite
		}
	}
	return e
}
func broadcast(r *runState, event Envelope) {
	for ch := range r.subscribers {
		select {
		case ch <- event:
		default:
			close(ch)
			delete(r.subscribers, ch)
		}
	}
}
func (s *Server) emit(r *runState, kind string, data any) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.active != r {
		return
	}
	s.emitLocked(r, kind, data)
}
func (s *Server) execute(ctx context.Context, r *runState, model Model, ids []string) {
	nextIndex := 0
	for i, id := range ids {
		if ctx.Err() != nil {
			break
		}
		problem := s.problems[id]
		nextIndex = i + 1
		s.mu.Lock()
		current := problem.public
		r.snapshot.Current = &current
		r.snapshot.Frontier = []FrontierItem{}
		r.snapshot.Candidates = []CandidateItem{}
		r.snapshot.DecisionContext = nil
		r.snapshot.BodyRejectionReason = ""
		r.snapshot.Step = 0
		r.snapshot.Phase = ""
		r.snapshot.PhaseStatus = ""
		r.snapshot.ValidationCandidateID = ""
		r.snapshot.ModelDeadline = nil
		r.snapshot.Metrics = map[string]any{"calls": 0, "candidates": 0, "max_calls": current.Budgets.MaxCalls, "max_candidates": current.Budgets.MaxCandidates, "max_tokens": current.Budgets.MaxTokens, "timeout_seconds": current.Budgets.TimeoutSeconds}
		r.candidateStep = 0
		seconds := current.Budgets.TimeoutSeconds
		deadline := time.Now().UTC().Add(time.Duration(seconds) * time.Second)
		r.snapshot.ProblemDeadline = &deadline
		current.Deadline = &deadline
		r.nodes = map[string]node{}
		s.emitLocked(r, "problem_started", current)
		s.mu.Unlock()
		q := runner.Request{Strategy: "beam", TraceDecisionContext: true, TokenTrace: func(event tokensynth.Event) { s.searchEvent(r, event) }}
		if problem.config.HeldoutPath != "" {
			q.TokenHeldout = func() ([]tokenspec.Test, error) {
				f, e := os.Open(problem.config.HeldoutPath)
				if e != nil {
					return nil, e
				}
				defer f.Close()
				return tokenspec.DecodeTests(f, problem.spec)
			}
		}
		result := s.cfg.Run(ctx, problem.spec, model.NewRanker(), q)
		s.mu.Lock()
		r.snapshot.Completed++
		if result.Status == "success" {
			r.snapshot.Succeeded++
		} else if result.Status != "cancelled" {
			r.snapshot.Failed++
		}
		r.snapshot.Results = append(r.snapshot.Results, ProblemResult{ProblemID: id, Result: result})
		s.emitLocked(r, "problem_finished", r.snapshot.Results[len(r.snapshot.Results)-1])
		s.mu.Unlock()
		if ctx.Err() != nil {
			break
		}
	}
	if ctx.Err() != nil {
		s.mu.Lock()
		for _, id := range ids[nextIndex:] {
			status := "not_run_cancelled"
			if r.storageFailed {
				status = "not_run_storage_error"
			}
			item := ProblemResult{ProblemID: id, Result: runner.Result{Engine: "token", CandidateUnit: "go_lexical_token", Status: status}}
			r.snapshot.Results = append(r.snapshot.Results, item)
			r.snapshot.NotRun++
			s.emitLocked(r, "problem_skipped", item)
		}
		s.mu.Unlock()
	}
	s.finish(r, ctx.Err() != nil)
}
func (s *Server) searchEvent(r *runState, e tokensynth.Event) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.active != r {
		return
	}
	if e.ID != "" && e.Lexeme != "" && (e.Reason == "available" || e.Reason == "forced" || e.Reason == "active" || e.Reason == "public_passed") {
		r.nodes[e.ID] = node{e.ParentID, e.Lexeme}
	}
	if e.Step > 0 {
		r.snapshot.Step = e.Step
		r.snapshot.Metrics["calls"] = e.Calls
		r.snapshot.Metrics["candidates"] = e.Candidates
	}
	if e.Reason == "decision_context" {
		r.snapshot.DecisionContext = e.DecisionContext
	}
	if e.Reason == "body_rejected" {
		switch e.RejectionReason {
		case "unused_expression", "branch_outside_loop", "statement_limit", "nesting_limit":
			r.snapshot.BodyRejectionReason = string(e.RejectionReason)
		}
	}
	if e.Reason == "available" {
		if e.Step != r.candidateStep {
			r.snapshot.Candidates = []CandidateItem{}
			r.snapshot.DecisionContext = nil
			r.candidateStep = e.Step
		}
		r.snapshot.Candidates = append(r.snapshot.Candidates, CandidateItem{ID: e.ID, ParentID: e.ParentID, Lexeme: e.Lexeme, TokenKind: e.TokenKind, Reason: e.Reason, Code: assemble(r.nodes, e.ID)})
	}
	if e.Reason == "ranked" || e.Reason == "active" || e.Reason == "pruned" || e.Reason == "pruned_strict" || e.Reason == "duplicate" || e.Reason == "public_passed" || e.Reason == "public_failed" {
		for i := range r.snapshot.Candidates {
			c := &r.snapshot.Candidates[i]
			if c.ID == e.ID {
				if e.Rank > 0 {
					c.Rank = e.Rank
				}
				if e.Score != nil {
					c.Score = e.Score
				}
				c.Reason = e.Reason
				break
			}
		}
	}
	if e.Reason == "model_started" {
		deadline := time.Now().UTC().Add(60 * time.Second)
		if r.snapshot.ProblemDeadline != nil && r.snapshot.ProblemDeadline.Before(deadline) {
			deadline = *r.snapshot.ProblemDeadline
		}
		r.snapshot.ModelDeadline = &deadline
		r.snapshot.Phase = "model"
		r.snapshot.PhaseStatus = "started"
	}
	if e.Reason == "model_finished" {
		r.snapshot.ModelDeadline = nil
		r.snapshot.Phase = "model"
		r.snapshot.PhaseStatus = "finished"
	}
	if e.Phase != "" {
		r.snapshot.Phase = e.Phase
		r.snapshot.PhaseStatus = e.Status
	}
	if e.Reason == "validation_started" {
		r.snapshot.ValidationCandidateID = e.ID
	}
	if e.Reason == "validation_finished" {
		r.snapshot.ValidationCandidateID = ""
	}
	if e.Reason == "frontier" {
		front := make([]FrontierItem, 0, len(e.Frontier))
		keep := map[string]bool{}
		for _, p := range e.Frontier {
			front = append(front, FrontierItem{ID: p.ID, ParentID: p.ParentID, Rank: p.Rank, TokenCount: p.TokenCount, Code: assemble(r.nodes, p.ID)})
			for id := p.ID; id != "" && id != "root" && !keep[id]; {
				keep[id] = true
				n, ok := r.nodes[id]
				if !ok {
					break
				}
				id = n.parent
			}
		}
		r.snapshot.Frontier = front
		for id := range r.nodes {
			if !keep[id] {
				delete(r.nodes, id)
			}
		}
		r.snapshot.Metrics["calls"] = e.Calls
		r.snapshot.Metrics["candidates"] = e.Candidates
	}
	kind := "search"
	if e.Phase != "" {
		kind = "verification"
	}
	s.emitLocked(r, kind, e)
}
func assemble(nodes map[string]node, id string) string {
	var parts []string
	seen := map[string]bool{}
	for id != "" && id != "root" && !seen[id] {
		seen[id] = true
		n, ok := nodes[id]
		if !ok {
			break
		}
		parts = append(parts, n.lexeme)
		id = n.parent
	}
	for i, j := 0, len(parts)-1; i < j; i, j = i+1, j-1 {
		parts[i], parts[j] = parts[j], parts[i]
	}
	var text string
	for i, p := range parts {
		if i > 0 {
			text += " "
		}
		text += p
	}
	return text
}
func (s *Server) finish(r *runState, cancelled bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.active != r {
		return
	}
	if e := r.journal.Sync(); e != nil {
		r.storageFailed = true
	}
	switch {
	case r.storageFailed:
		r.snapshot.Status = "storage_error"
	case cancelled:
		r.snapshot.Status = "cancelled"
	default:
		r.snapshot.Status = "completed"
	}
	now := time.Now().UTC()
	r.snapshot.FinishedAt = &now
	r.snapshot.Seq++
	terminal := Envelope{Seq: r.snapshot.Seq, RunID: r.snapshot.RunID, Kind: "run_finished", Data: map[string]any{"status": r.snapshot.Status, "completed": r.snapshot.Completed}}
	terminalStatus := r.snapshot.Status
	if e := record(r, terminal); e != nil {
		r.snapshot.Status = "storage_error"
	}
	if e := r.journal.Sync(); e != nil {
		r.snapshot.Status = "storage_error"
	}
	if e := r.journal.Close(); e != nil {
		r.snapshot.Status = "storage_error"
	}
	if e := finalizeTrace(r.tmpTrace, r.finalTrace); e != nil {
		r.snapshot.Status = "storage_error"
	}
	if e := saveSnapshot(r.path, r.snapshot); e != nil {
		r.snapshot.Status = "storage_error"
	}
	if r.snapshot.Status == "storage_error" && terminalStatus != "storage_error" {
		r.snapshot.Seq++
		terminal = Envelope{Seq: r.snapshot.Seq, RunID: r.snapshot.RunID, Kind: "storage_error", Data: map[string]any{"status": "storage_error"}}
		trace := r.tmpTrace
		if _, e := os.Stat(trace); e != nil {
			trace = r.finalTrace
		}
		if f, e := os.OpenFile(trace, os.O_WRONLY|os.O_APPEND, 0600); e == nil {
			data, _ := json.Marshal(terminal)
			_, _ = f.Write(append(data, '\n'))
			_ = f.Sync()
			_ = f.Close()
		}
		_ = saveSnapshot(r.path, r.snapshot)
	}
	if r.snapshot.Status == "storage_error" {
		if len(s.failed) >= 32 {
			for id := range s.failed {
				delete(s.failed, id)
				break
			}
		}
		s.failed[r.snapshot.RunID] = copySnapshot(r.snapshot)
	}
	broadcast(r, terminal)
	for ch := range r.subscribers {
		close(ch)
		delete(r.subscribers, ch)
	}
	r.cancel()
	s.active = nil
	close(r.done)
}
func saveSnapshot(path string, snapshot Snapshot) error {
	data, err := json.MarshalIndent(snapshot, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(data, '\n'), 0600)
}
func finalizeTrace(tmp, destination string) error {
	src, err := os.Open(tmp)
	if err != nil {
		return err
	}
	dst, err := os.OpenFile(destination, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		_ = src.Close()
		return err
	}
	_, copyErr := io.Copy(dst, src)
	if copyErr == nil {
		copyErr = dst.Sync()
	}
	closeErr := dst.Close()
	srcErr := src.Close()
	if copyErr != nil {
		return copyErr
	}
	if closeErr != nil {
		return closeErr
	}
	if srcErr != nil {
		return srcErr
	}
	return os.Remove(tmp)
}
func (s *Server) Close(ctx context.Context) error {
	s.mu.Lock()
	r := s.active
	if r != nil {
		r.cancel()
	}
	s.mu.Unlock()
	if r == nil {
		return nil
	}
	select {
	case <-r.done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
