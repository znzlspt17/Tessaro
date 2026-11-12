package application

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"tessaro/internal/app"
	"tessaro/internal/benchmark"
	"tessaro/internal/compare"
	"tessaro/internal/hypothesis"
	"tessaro/internal/rank"
	"tessaro/internal/runner"
	"tessaro/internal/spec"
	"tessaro/internal/synth"
	"tessaro/internal/tokenspec"
	"tessaro/internal/tokensynth"
	"os"
	"path/filepath"
	"sync"
	"time"
)

type Config struct {
	OutputDir string
	Resolve   func(context.Context, compare.Model) (rank.Ranker, error)
}

type Request struct {
	Kind, Engine, Strategy, ManifestPath, SpecPath, HeldoutPath, ProblemID string
	Model                                                                  compare.Model
	Manifest                                                               *compare.Manifest
	Options                                                                runner.Request
}

type PublicProblem struct {
	ID          string `json:"id"`
	Label       string `json:"label"`
	Intent      string `json:"intent"`
	PublicTests any    `json:"public_tests"`
}
type FrontierItem struct {
	ID         string `json:"id"`
	ParentID   string `json:"parent_id,omitempty"`
	Rank       int    `json:"rank"`
	TokenCount int    `json:"token_count"`
	Code       string `json:"code"`
}
type CandidateItem struct {
	ID                  string   `json:"id"`
	ParentID            string   `json:"parent_id,omitempty"`
	Rank                int      `json:"rank,omitempty"`
	Score               *float64 `json:"score,omitempty"`
	Lexeme              string   `json:"lexeme"`
	TokenKind           string   `json:"token_kind,omitempty"`
	Reason              string   `json:"reason"`
	RejectionReason     string   `json:"rejection_reason,omitempty"`
	RejectionTokenIndex *int     `json:"rejection_token_index,omitempty"`
	Code                string   `json:"code"`
}
type ProblemResult struct {
	ProblemID string        `json:"problem_id"`
	Result    runner.Result `json:"result"`
}
type Snapshot struct {
	RunID               string          `json:"run_id"`
	Kind                string          `json:"kind"`
	SchemaVersion       int             `json:"schema_version"`
	JobKind             string          `json:"job_kind"`
	Status              string          `json:"status"`
	Error               string          `json:"error,omitempty"`
	Model               string          `json:"model"`
	Problem             string          `json:"problem"`
	Seq                 uint64          `json:"seq"`
	StartedAt           time.Time       `json:"started_at"`
	FinishedAt          *time.Time      `json:"finished_at,omitempty"`
	Total               int             `json:"total"`
	Completed           int             `json:"completed"`
	RequestsUsed        int             `json:"requests_used"`
	RequestLimit        int             `json:"request_limit"`
	Current             *PublicProblem  `json:"current"`
	Frontier            []FrontierItem  `json:"frontier"`
	Candidates          []CandidateItem `json:"candidates"`
	Step                int             `json:"step"`
	DecisionContext     map[string]any  `json:"decision_context,omitempty"`
	ChosenID            string          `json:"chosen_id,omitempty"`
	DecisionStatus      string          `json:"decision_status,omitempty"`
	ForcedID            string          `json:"forced_id,omitempty"`
	ForcedLexeme        string          `json:"forced_lexeme,omitempty"`
	RejectionReason     string          `json:"declaration_rejection_reason,omitempty"`
	BodyRejectionReason string          `json:"body_rejection_reason,omitempty"`
	Phase               string          `json:"phase,omitempty"`
	Results             []ProblemResult `json:"results"`
	Payload             json.RawMessage `json:"payload,omitempty"`
}
type Envelope struct {
	Seq   uint64          `json:"seq"`
	RunID string          `json:"run_id"`
	Kind  string          `json:"kind"`
	Data  json.RawMessage `json:"data"`
}
type Manager struct {
	mu            sync.Mutex
	cfg           Config
	snapshot      Snapshot
	cancel        context.CancelFunc
	done          chan struct{}
	changed       chan struct{}
	journal       *os.File
	journalOffset int64
	path          string
	tmpTrace      string
	nodes         map[string]node
	candidateStep int
	storageFailed bool
	closed        bool
	failed        map[string]Snapshot
}
type node struct{ parent, lexeme string }

func New(cfg Config) *Manager {
	if cfg.OutputDir == "" {
		cfg.OutputDir = "output"
	}
	return &Manager{cfg: cfg, changed: make(chan struct{}, 1), failed: make(map[string]Snapshot)}
}
func (m *Manager) Changed() <-chan struct{} { return m.changed }
func (m *Manager) notify() {
	select {
	case m.changed <- struct{}{}:
	default:
	}
}
func (m *Manager) Snapshot() Snapshot {
	m.mu.Lock()
	defer m.mu.Unlock()
	b, _ := json.Marshal(m.snapshot)
	var s Snapshot
	_ = json.Unmarshal(b, &s)
	return s
}

// 2026-11-07: 쓰기 완료 이벤트를 기록한 뒤 알림을 병합해 느린 화면이 탐색을 차단하지 않게 한다.
func (m *Manager) emitLocked(kind string, value any) {
	if m.storageFailed {
		return
	}
	data, err := json.Marshal(value)
	m.snapshot.Seq++
	event := Envelope{Seq: m.snapshot.Seq, RunID: m.snapshot.RunID, Kind: kind, Data: data}
	if err == nil {
		var encoded []byte
		encoded, err = json.Marshal(event)
		if err == nil {
			encoded = append(encoded, '\n')
			if len(encoded) > 8<<20 {
				m.storageFailed = true
				if m.cancel != nil {
					m.cancel()
				}
				m.notify()
				return
			}
			var written int
			written, err = m.journal.Write(encoded)
			if err == nil && written != len(encoded) {
				err = io.ErrShortWrite
			}
			if err == nil {
				m.journalOffset += int64(written)
			}
		}
	}
	if err != nil {
		m.snapshot.Seq--
		m.storageFailed = true
		if m.cancel != nil {
			m.cancel()
		}
	}
	m.notify()
}
func (m *Manager) Start(q Request) (string, error) {
	if q.Kind != "search" && q.Kind != "compare" && q.Kind != "benchmark" && q.Kind != "hypothesis" {
		return "", errors.New("invalid_job")
	}
	if m.cfg.Resolve == nil {
		return "", errors.New("model_unavailable")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return "", errors.New("closed")
	}
	if m.cancel != nil {
		return "", errors.New("busy")
	}
	now := time.Now().UTC()
	s := Snapshot{Kind: "desktop_run", SchemaVersion: 1, JobKind: q.Kind, Status: "running", Model: q.Model.ID, Problem: q.ProblemID, StartedAt: now, Frontier: []FrontierItem{}, Candidates: []CandidateItem{}, Results: []ProblemResult{}}
	if q.Kind == "hypothesis" {
		s.Model = ""
	}
	path, err := app.WriteResultWithPath(io.Discard, m.cfg.OutputDir, now, s)
	if err != nil {
		return "", errors.New("storage_error")
	}
	s.RunID = filepath.Base(path)
	tmp := path + ".trace.tmp"
	j, err := os.OpenFile(tmp, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		_ = os.Remove(path)
		return "", errors.New("storage_error")
	}
	ctx, cancel := context.WithCancel(context.Background())
	m.snapshot, m.cancel, m.done, m.path, m.tmpTrace, m.journal = s, cancel, make(chan struct{}), path, tmp, j
	m.nodes, m.candidateStep, m.storageFailed = map[string]node{}, 0, false
	m.journalOffset = 0
	if err = saveSnapshot(path, s); err != nil {
		_ = j.Close()
		_ = os.Remove(tmp)
		_ = os.Remove(path)
		m.cancel = nil
		cancel()
		return "", errors.New("storage_error")
	}
	m.emitLocked("run_started", map[string]any{"job_kind": q.Kind})
	go m.execute(ctx, q)
	return s.RunID, nil
}
func (m *Manager) Cancel() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.cancel == nil {
		return errors.New("not_running")
	}
	if m.snapshot.Status != "cancelling" {
		m.snapshot.Status = "cancelling"
		m.emitLocked("cancelling", nil)
		m.cancel()
	}
	return nil
}
func (m *Manager) Close(ctx context.Context) error {
	m.mu.Lock()
	m.closed = true
	c, done := m.cancel, m.done
	if c != nil {
		c()
	}
	m.mu.Unlock()
	if c == nil {
		return nil
	}
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (m *Manager) execute(ctx context.Context, q Request) {
	var err error
	switch q.Kind {
	case "search":
		err = m.search(ctx, q)
	case "compare":
		err = m.comparison(ctx, q)
	case "benchmark":
		err = m.benchmark(ctx, q)
	case "hypothesis":
		err = m.hypothesis(ctx, q)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	status := "completed"
	if errors.Is(ctx.Err(), context.Canceled) || errors.Is(err, context.Canceled) {
		status = "cancelled"
	} else if errors.Is(ctx.Err(), context.DeadlineExceeded) || errors.Is(err, context.DeadlineExceeded) {
		status = "timeout"
	} else if err != nil {
		status = "failed"
		m.snapshot.Error = safeError(err)
	}
	if m.storageFailed {
		status = "storage_error"
	}
	m.snapshot.Status = status
	now := time.Now().UTC()
	m.snapshot.FinishedAt = &now
	// 2026-11-07: 마지막 완전 이벤트 위치만 사용해 부분 쓰기 후 중간 JSON 손상을 남기지 않는다.
	terminalOffset := m.journalOffset
	priorSeq := m.snapshot.Seq
	m.emitLocked("run_finished", map[string]any{"status": status})
	if m.snapshot.Seq == priorSeq {
		m.snapshot.Seq++
	}
	if m.storageFailed {
		m.snapshot.Status = "storage_error"
	}
	syncErr := m.journal.Sync()
	closeErr := m.journal.Close()
	if syncErr != nil || closeErr != nil {
		m.snapshot.Status = "storage_error"
	}
	if err := finalizeTrace(m.tmpTrace, m.path+".trace.jsonl"); err != nil {
		m.snapshot.Status = "storage_error"
	}
	if m.snapshot.Status == "storage_error" {
		_ = rewriteStorageTerminal(m.tmpTrace, m.path+".trace.jsonl", terminalOffset, m.snapshot)
	}
	if err := saveSnapshot(m.path, m.snapshot); err != nil {
		m.snapshot.Status = "storage_error"
		_ = rewriteStorageTerminal(m.tmpTrace, m.path+".trace.jsonl", terminalOffset, m.snapshot)
		light := m.snapshot
		light.Payload = nil
		_ = saveSnapshot(m.path, light)
	}
	if m.snapshot.Status == "storage_error" {
		if len(m.failed) >= 32 {
			for id := range m.failed {
				delete(m.failed, id)
				break
			}
		}
		m.failed[m.snapshot.RunID] = m.snapshot
	}
	if m.cancel != nil {
		m.cancel()
	}
	m.cancel = nil
	close(m.done)
	m.notify()
}
func safeError(err error) string {
	switch err.Error() {
	case "invalid_job", "invalid_input", "model_unavailable", "unsupported_model", "storage_error", "model_provider_closed", "unsupported_local_model", "local_runtime_not_configured", "select_real_model", "worker_unavailable", "worker_start_failed", "worker_start_timeout", "worker_invalid_response", "worker_request_timeout", "worker_inference_failed", "worker_protocol_error", "worker_initialization_failed":
		return err.Error()
	}
	return "execution_error"
}
func saveSnapshot(path string, snap Snapshot) error {
	b, err := json.MarshalIndent(snap, "", "  ")
	if err != nil {
		return err
	}
	if len(b)+1 > 32<<20 {
		return errors.New("snapshot_too_large")
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".snapshot-*")
	if err != nil {
		return err
	}
	name := f.Name()
	defer os.Remove(name)
	if err = f.Chmod(0600); err == nil {
		_, err = f.Write(append(b, '\n'))
	}
	if err == nil {
		err = f.Sync()
	}
	if e := f.Close(); err == nil {
		err = e
	}
	if err != nil {
		return err
	}
	return os.Rename(name, path)
}

// 2026-11-07: 마감 중 실패하면 앞선 성공 terminal을 대체하여 trace와 상태를 일치시킨다.
func rewriteStorageTerminal(tmp, dst string, offset int64, s Snapshot) error {
	path := tmp
	if _, e := os.Stat(path); e != nil {
		path = dst
	}
	f, e := os.OpenFile(path, os.O_WRONLY, 0600)
	if e != nil {
		return e
	}
	defer f.Close()
	if e = f.Truncate(offset); e != nil {
		return e
	}
	if _, e = f.Seek(offset, io.SeekStart); e != nil {
		return e
	}
	data, _ := json.Marshal(map[string]string{"status": "storage_error"})
	b, e := json.Marshal(Envelope{Seq: s.Seq, RunID: s.RunID, Kind: "storage_error", Data: data})
	if e != nil {
		return e
	}
	b = append(b, '\n')
	n, e := f.Write(b)
	if e == nil && n != len(b) {
		e = io.ErrShortWrite
	}
	if e != nil {
		return e
	}
	return f.Sync()
}
func finalizeTrace(src, dst string) error {
	// 불완전한 journal을 복구할 수 있도록 최종 파일이 이미 있으면 덮어쓰지 않는다.
	if _, err := os.Stat(dst); err == nil {
		return errors.New("trace_exists")
	} else if !os.IsNotExist(err) {
		return err
	}
	return os.Rename(src, dst)
}

func decodeCompare(path string) (compare.Manifest, error) {
	f, e := os.Open(path)
	if e != nil {
		return compare.Manifest{}, e
	}
	defer f.Close()
	return compare.Decode(f)
}
func (m *Manager) model(ctx context.Context, model compare.Model) (rank.Ranker, error) {
	m.mu.Lock()
	m.snapshot.Phase = "model_preparing"
	m.emitLocked("phase", map[string]string{"phase": "model_preparing"})
	m.mu.Unlock()
	if model.Ranker == "fake" {
		m.mu.Lock()
		m.snapshot.Phase = "model_error"
		m.emitLocked("phase", map[string]string{"phase": "model_error"})
		m.mu.Unlock()
		return nil, errors.New("unsupported_model")
	}
	r, err := m.cfg.Resolve(ctx, model)
	m.mu.Lock()
	if err != nil {
		m.snapshot.Phase = "model_error"
	} else {
		m.snapshot.Phase = "ready"
	}
	m.emitLocked("phase", map[string]string{"phase": m.snapshot.Phase})
	m.mu.Unlock()
	return r, err
}
func (m *Manager) comparison(ctx context.Context, q Request) error {
	manifest, err := decodeCompare(q.ManifestPath)
	if err != nil {
		return errors.New("invalid_input")
	}
	if q.Manifest != nil {
		manifest = *q.Manifest
	}
	for _, model := range manifest.Models {
		if model.Ranker == "fake" {
			return errors.New("unsupported_model")
		}
	}
	m.mu.Lock()
	m.snapshot.Total = len(compare.Plan(manifest))
	m.mu.Unlock()
	report := compare.RunWithOptions(ctx, filepath.Dir(q.ManifestPath), manifest, func(e any) {
		m.mu.Lock()
		defer m.mu.Unlock()
		b, _ := json.Marshal(e)
		var event struct {
			TrialIndex int `json:"trial_index"`
		}
		_ = json.Unmarshal(b, &event)
		if event.TrialIndex > 0 && event.TrialIndex-1 > m.snapshot.Completed {
			m.snapshot.Completed = event.TrialIndex - 1
		}
		m.emitLocked("comparison", e)
	}, m.model, q.Options, true)
	m.mu.Lock()
	m.snapshot.Completed = report.Attempted
	m.snapshot.Payload, _ = json.Marshal(report)
	m.emitLocked("report", map[string]any{"job_kind": "compare", "attempted": report.Attempted, "total": report.Total})
	m.mu.Unlock()
	return nil
}
func (m *Manager) benchmark(ctx context.Context, q Request) error {
	f, err := os.Open(q.ManifestPath)
	if err != nil {
		return errors.New("invalid_input")
	}
	manifest, err := benchmark.Decode(f)
	_ = f.Close()
	if err != nil {
		return errors.New("invalid_input")
	}
	m.mu.Lock()
	m.snapshot.Total = len(manifest.Problems)
	m.mu.Unlock()
	r, err := m.model(ctx, q.Model)
	if err != nil {
		return err
	}
	timeout := q.Options.Timeout
	if timeout <= 0 {
		timeout = 300 * time.Second
	}
	benchmarkCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	report := benchmark.RunWithProgress(benchmarkCtx, manifest, r, func(done, total int) {
		m.mu.Lock()
		defer m.mu.Unlock()
		m.snapshot.Completed = done
		m.emitLocked("progress", map[string]int{"completed": done, "total": total})
	})
	m.mu.Lock()
	m.snapshot.Completed = len(report.Items)
	m.snapshot.Payload, _ = json.Marshal(report)
	m.emitLocked("report", map[string]any{"job_kind": "benchmark", "completed": len(report.Items), "total": report.Total})
	m.mu.Unlock()
	return benchmarkCtx.Err()
}

// 2026-11-08: manifest가 고정한 모델만 지연 준비하고 중단된 평가도 보고서로 보존한다.
func (m *Manager) hypothesis(ctx context.Context, q Request) error {
	report, runErr := hypothesis.Run(ctx, q.ManifestPath, func(ctx context.Context, model compare.Model) (rank.Ranker, error) {
		m.mu.Lock()
		m.snapshot.Model = model.ID
		m.mu.Unlock()
		return m.model(ctx, model)
	}, func(p hypothesis.Progress) {
		m.mu.Lock()
		defer m.mu.Unlock()
		m.snapshot.Phase = p.Phase
		m.snapshot.Completed, m.snapshot.Total = p.Completed, p.Total
		m.snapshot.RequestsUsed, m.snapshot.RequestLimit = p.RequestsUsed, p.RequestLimit
		if p.ProblemID == "" {
			m.snapshot.Current = nil
		} else {
			m.snapshot.Current = &PublicProblem{ID: p.ProblemID}
		}
		m.emitLocked("progress", map[string]any{"phase": p.Phase, "problem_id": p.ProblemID, "completed": p.Completed, "total": p.Total, "requests_used": p.RequestsUsed, "request_limit": p.RequestLimit})
		if p.Event != nil {
			m.emitLocked("hypothesis_detail", p.Event)
		}
	})
	m.mu.Lock()
	m.snapshot.RequestsUsed, m.snapshot.RequestLimit = report.RequestsUsed, report.RequestLimit
	m.snapshot.Payload, _ = json.Marshal(report)
	m.emitLocked("report", map[string]any{"job_kind": "hypothesis", "status": report.Status})
	m.mu.Unlock()
	if err := os.WriteFile(m.path+".summary.md", []byte(report.Summary), 0600); err != nil {
		m.mu.Lock()
		m.storageFailed = true
		m.mu.Unlock()
	}
	if runErr != nil {
		return runErr
	}
	switch report.Status {
	case "completed":
		return nil
	case "cancelled":
		return context.Canceled
	default:
		return errors.New("execution_error")
	}
}

func assemble(nodes map[string]node, id string) string {
	parts := []string{}
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
	result := ""
	for i := len(parts) - 1; i >= 0; i-- {
		if result != "" {
			result += " "
		}
		result += parts[i]
	}
	return result
}

func (m *Manager) tokenEvent(e tokensynth.Event) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if e.Reason == "declaration_rejected" {
		m.snapshot.RejectionReason = string(e.RejectionReason)
	}
	if e.Reason == "body_rejected" {
		m.snapshot.BodyRejectionReason = string(e.RejectionReason)
	}
	preRankRejected := e.ID != "" && e.Lexeme != "" && (e.Reason == "body_rejected" || e.Reason == "declaration_rejected")
	if e.ID != "" && e.Lexeme != "" && (e.Reason == "available" || e.Reason == "forced" || e.Reason == "active" || e.Reason == "public_passed" || preRankRejected) {
		m.nodes[e.ID] = node{e.ParentID, e.Lexeme}
	}
	if e.Step > 0 {
		m.snapshot.Step = e.Step
	}
	if e.Reason == "decision_context" {
		m.snapshot.DecisionContext = e.DecisionContext
		m.snapshot.ChosenID = ""
		m.snapshot.ForcedID = ""
		m.snapshot.ForcedLexeme = ""
		m.snapshot.DecisionStatus = "prepared"
	}
	if e.Reason == "model_started" {
		m.snapshot.ChosenID = ""
		m.snapshot.DecisionStatus = "calling"
	}
	if e.Reason == "decision_result" {
		m.snapshot.ChosenID = e.ChosenID
		m.snapshot.DecisionStatus = e.DecisionStatus
	}
	if e.Reason == "forced" {
		if e.Step != m.candidateStep {
			m.snapshot.Candidates = nil
			m.candidateStep = e.Step
		}
		m.snapshot.ChosenID = ""
		m.snapshot.DecisionStatus = "forced"
		m.snapshot.ForcedID = e.ID
		m.snapshot.ForcedLexeme = e.Lexeme
		m.snapshot.DecisionContext = nil
	}
	// 2026-11-07: 모델에 보내기 전 제외된 후보도 같은 단계 목록에서 원인과 함께 보존한다.
	if e.Reason == "available" || preRankRejected {
		if m.snapshot.DecisionStatus == "forced" {
			m.snapshot.DecisionStatus = ""
			m.snapshot.ForcedID = ""
			m.snapshot.ForcedLexeme = ""
		}
		if e.Step != m.candidateStep {
			m.snapshot.Candidates = []CandidateItem{}
			m.snapshot.DecisionContext = nil
			m.snapshot.ChosenID = ""
			m.snapshot.DecisionStatus = ""
			m.snapshot.ForcedID = ""
			m.snapshot.ForcedLexeme = ""
			m.candidateStep = e.Step
		}
		found := false
		for _, candidate := range m.snapshot.Candidates {
			if candidate.ID == e.ID {
				found = true
				break
			}
		}
		if !found {
			m.snapshot.Candidates = append(m.snapshot.Candidates, CandidateItem{ID: e.ID, ParentID: e.ParentID, Lexeme: e.Lexeme, TokenKind: e.TokenKind, Reason: e.Reason, RejectionReason: string(e.RejectionReason), RejectionTokenIndex: e.RejectionTokenIndex, Code: assemble(m.nodes, e.ID)})
		}
	}
	for i := range m.snapshot.Candidates {
		c := &m.snapshot.Candidates[i]
		if c.ID == e.ID && e.Reason != "available" {
			if e.Rank > 0 {
				c.Rank = e.Rank
			}
			if e.Score != nil {
				c.Score = e.Score
			}
			c.Reason = e.Reason
			if e.RejectionReason != "" {
				c.RejectionReason = string(e.RejectionReason)
			}
			if e.RejectionTokenIndex != nil {
				c.RejectionTokenIndex = e.RejectionTokenIndex
			}
			break
		}
	}
	if e.Reason == "frontier" {
		front := make([]FrontierItem, 0, len(e.Frontier))
		for _, p := range e.Frontier {
			front = append(front, FrontierItem{ID: p.ID, ParentID: p.ParentID, Rank: p.Rank, TokenCount: p.TokenCount, Code: assemble(m.nodes, p.ID)})
		}
		m.snapshot.Frontier = front
	}
	m.emitLocked("search", e)
}

func (m *Manager) search(ctx context.Context, q Request) error {
	engine := q.Engine
	if engine == "" {
		engine = "token"
	}
	if engine != "token" && engine != "ast" {
		return errors.New("invalid_input")
	}
	items := []compare.Problem{}
	base := ""
	if q.SpecPath != "" {
		items = append(items, compare.Problem{ID: q.ProblemID, Spec: q.SpecPath, Heldout: q.HeldoutPath})
		if items[0].ID == "" {
			items[0].ID = filepath.Base(q.SpecPath)
		}
	} else {
		manifest, err := decodeCompare(q.ManifestPath)
		if err != nil {
			return errors.New("invalid_input")
		}
		base = filepath.Dir(q.ManifestPath)
		if engine != manifest.Engine {
			return errors.New("invalid_input")
		}
		for _, p := range manifest.Problems {
			if q.ProblemID == "all" || q.ProblemID == p.ID {
				items = append(items, p)
			}
		}
	}
	if len(items) == 0 {
		return errors.New("invalid_input")
	}
	m.mu.Lock()
	m.snapshot.Total = len(items)
	m.mu.Unlock()
	r, err := m.model(ctx, q.Model)
	if err != nil {
		return err
	}
	for i, p := range items {
		if ctx.Err() != nil {
			m.mu.Lock()
			for _, skipped := range items[i:] {
				res := ProblemResult{ProblemID: skipped.ID, Result: runner.Result{Engine: engine, Status: "not_run_cancelled"}}
				m.snapshot.Results = append(m.snapshot.Results, res)
				m.emitLocked("problem_skipped", res)
			}
			m.mu.Unlock()
			break
		}
		path := p.Spec
		if !filepath.IsAbs(path) {
			path = filepath.Join(base, path)
		}
		f, e := os.Open(path)
		if e != nil {
			return errors.New("invalid_input")
		}
		var result runner.Result
		opt := q.Options
		opt.Strategy = q.Strategy
		if opt.Strategy == "" {
			opt.Strategy = "beam"
		}
		opt.TraceCode = true
		opt.TraceDecisionContext = true
		hold := p.Heldout
		if hold != "" && !filepath.IsAbs(hold) {
			hold = filepath.Join(base, hold)
		}
		if engine == "token" {
			prob, de := tokenspec.Decode(f)
			_ = f.Close()
			if de != nil {
				return errors.New("invalid_input")
			}
			m.problemStart(PublicProblem{ID: p.ID, Label: p.ID, Intent: prob.Intent, PublicTests: prob.PublicTests})
			opt.TokenTrace = m.tokenEvent
			if hold != "" {
				opt.TokenHeldout = func() ([]tokenspec.Test, error) {
					h, e := os.Open(hold)
					if e != nil {
						return nil, e
					}
					defer h.Close()
					return tokenspec.DecodeTests(h, prob)
				}
			}
			result = runner.RunToken(ctx, prob, r, opt)
		} else {
			prob, de := spec.Decode(f)
			_ = f.Close()
			if de != nil {
				return errors.New("invalid_input")
			}
			m.problemStart(PublicProblem{ID: p.ID, Label: p.ID, Intent: prob.Intent, PublicTests: prob.PublicTests})
			opt.Trace = m.astEvent
			if hold != "" {
				opt.Heldout = func() ([]spec.Test, error) {
					h, e := os.Open(hold)
					if e != nil {
						return nil, e
					}
					defer h.Close()
					return spec.DecodeTests(h, prob)
				}
			}
			result = runner.Run(ctx, prob, r, opt)
		}
		m.mu.Lock()
		m.snapshot.Completed++
		item := ProblemResult{ProblemID: p.ID, Result: result}
		m.snapshot.Results = append(m.snapshot.Results, item)
		m.emitLocked("problem_finished", item)
		m.mu.Unlock()
	}
	return nil
}
func (m *Manager) problemStart(p PublicProblem) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.snapshot.Current = &p
	m.snapshot.Frontier = []FrontierItem{}
	m.snapshot.Candidates = []CandidateItem{}
	m.snapshot.DecisionContext = nil
	m.snapshot.ChosenID = ""
	m.snapshot.DecisionStatus = ""
	m.snapshot.ForcedID = ""
	m.snapshot.ForcedLexeme = ""
	m.snapshot.RejectionReason = ""
	m.snapshot.BodyRejectionReason = ""
	m.snapshot.Step = 0
	m.candidateStep = 0
	m.nodes = map[string]node{}
	m.emitLocked("problem_started", p)
}

// 2026-11-07: AST 후보 이벤트도 토큰 화면과 동일한 후보·경로 모델에 투영한다.
func (m *Manager) astEvent(e synth.TraceEvent) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if e.Step != m.candidateStep {
		m.snapshot.Candidates = []CandidateItem{}
		m.snapshot.Frontier = []FrontierItem{}
		m.candidateStep = e.Step
	}
	m.snapshot.Step = e.Step
	found := false
	for i := range m.snapshot.Candidates {
		c := &m.snapshot.Candidates[i]
		if c.ID == e.ID {
			c.Reason = e.Reason
			c.Rank = e.Rank
			c.Score = e.Score
			if e.Code != "" {
				c.Code = e.Code
			}
			found = true
			break
		}
	}
	if !found {
		m.snapshot.Candidates = append(m.snapshot.Candidates, CandidateItem{ID: e.ID, ParentID: e.ParentID, Rank: e.Rank, Score: e.Score, Reason: e.Reason, Code: e.Code})
	}
	if e.Rank > 0 && e.Rank <= 3 && (e.Reason == "ranked" || e.Reason == "active" || e.Reason == "accepted" || e.Reason == "public_passed") {
		found = false
		for i := range m.snapshot.Frontier {
			if m.snapshot.Frontier[i].ID == e.ID {
				m.snapshot.Frontier[i].Code = e.Code
				found = true
				break
			}
		}
		if !found {
			m.snapshot.Frontier = append(m.snapshot.Frontier, FrontierItem{ID: e.ID, ParentID: e.ParentID, Rank: e.Rank, Code: e.Code})
		}
	}
	m.emitLocked("search", e)
}
