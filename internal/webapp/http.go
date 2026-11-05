package webapp

import (
	"context"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"mime"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

//go:embed assets
var embedded embed.FS
var errBusy = errors.New("run already active")
var errInvalid = errors.New("invalid selection")
var runIDPattern = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}\.\d{3}$`)

func localHost(hostport string) bool {
	host := hostport
	if h, _, e := net.SplitHostPort(hostport); e == nil {
		host = h
	}
	host = strings.Trim(host, "[]")
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
func fail(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]string{"error": message})
}
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if !localHost(r.Host) {
		fail(w, 403, "local host required")
		return
	}
	if origin := r.Header.Get("Origin"); origin != "" {
		u, e := url.Parse(origin)
		if e != nil || u.Host != r.Host || (u.Scheme != "http" && u.Scheme != "https") || u.Path != "" || u.RawQuery != "" || u.Fragment != "" {
			fail(w, 403, "origin not allowed")
			return
		}
	}
	if r.URL.Path == "/api/catalog" && r.Method == "GET" {
		s.catalog(w)
		return
	}
	if r.URL.Path == "/api/runs" && r.Method == "POST" {
		s.startHTTP(w, r)
		return
	}
	if r.URL.Path == "/api/history" && r.Method == "GET" {
		s.history(w)
		return
	}
	parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	if len(parts) >= 3 && parts[0] == "api" && (parts[1] == "runs" || parts[1] == "history") && runIDPattern.MatchString(parts[2]) {
		id := parts[2]
		if len(parts) == 3 && r.Method == "GET" {
			s.snapshotHTTP(w, id)
			return
		}
		if len(parts) == 4 {
			switch {
			case parts[1] == "runs" && parts[3] == "cancel" && r.Method == "POST":
				s.cancelHTTP(w, id)
				return
			case parts[1] == "runs" && parts[3] == "events" && r.Method == "GET":
				s.eventsHTTP(w, r, id)
				return
			case parts[3] == "trace" && r.Method == "GET":
				s.traceHTTP(w, r, id)
				return
			case parts[1] == "history" && parts[3] == "download" && r.Method == "GET":
				s.downloadHTTP(w, id)
				return
			}
		}
	}
	if strings.HasPrefix(r.URL.Path, "/api/") {
		fail(w, 404, "not found")
		return
	}
	if r.Method != "GET" && r.Method != "HEAD" {
		fail(w, 405, "method not allowed")
		return
	}
	name := strings.TrimPrefix(r.URL.Path, "/")
	if name == "" {
		name = "index.html"
	}
	switch name {
	case "index.html", "app.mjs", "state.mjs", "style.css":
	default:
		http.NotFound(w, r)
		return
	}
	assets, _ := fs.Sub(embedded, "assets")
	if strings.HasSuffix(name, ".mjs") {
		w.Header().Set("Content-Type", "text/javascript; charset=utf-8")
	}
	http.FileServer(http.FS(assets)).ServeHTTP(w, r)
}
func (s *Server) catalog(w http.ResponseWriter) {
	models := make([]catalogModel, 0, len(s.models))
	for _, m := range s.models {
		models = append(models, catalogModel{m.ID, m.Label})
	}
	sort.Slice(models, func(i, j int) bool { return models[i].ID < models[j].ID })
	problems := make([]PublicProblem, 0, len(s.order))
	for _, id := range s.order {
		problems = append(problems, s.problems[id].public)
	}
	writeJSON(w, 200, map[string]any{"models": models, "problems": problems, "all_count": len(problems)})
}
func (s *Server) startHTTP(w http.ResponseWriter, r *http.Request) {
	kind, _, e := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if e != nil || kind != "application/json" {
		fail(w, 415, "application/json required")
		return
	}
	d := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096))
	d.DisallowUnknownFields()
	var input struct {
		Model   string `json:"model"`
		Problem string `json:"problem"`
	}
	if e = d.Decode(&input); e != nil {
		fail(w, 400, "invalid request")
		return
	}
	var extra any
	if d.Decode(&extra) != io.EOF {
		fail(w, 400, "invalid request")
		return
	}
	id, e := s.start(input.Model, input.Problem)
	switch {
	case errors.Is(e, errBusy):
		fail(w, 409, "run already active")
	case errors.Is(e, errInvalid):
		fail(w, 400, "invalid selection")
	case e != nil:
		fail(w, 500, "run could not be started")
	default:
		writeJSON(w, 202, map[string]string{"run_id": id})
	}
}
func (s *Server) cancelHTTP(w http.ResponseWriter, id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.active == nil || s.active.snapshot.RunID != id {
		fail(w, 404, "active run not found")
		return
	}
	r := s.active
	if r.snapshot.Status != "cancelling" {
		r.snapshot.Status = "cancelling"
		s.emitLocked(r, "cancelling", map[string]any{})
		r.cancel()
	}
	writeJSON(w, 202, map[string]string{"status": "cancelling"})
}
func copySnapshot(in Snapshot) Snapshot {
	b, _ := json.Marshal(in)
	var out Snapshot
	_ = json.Unmarshal(b, &out)
	return out
}
func (s *Server) snapshot(id string) (Snapshot, error) {
	s.mu.Lock()
	if s.active != nil && s.active.snapshot.RunID == id {
		snap := copySnapshot(s.active.snapshot)
		s.mu.Unlock()
		return snap, nil
	}
	if snap, ok := s.failed[id]; ok {
		copy := copySnapshot(snap)
		s.mu.Unlock()
		return copy, nil
	}
	s.mu.Unlock()
	if !runIDPattern.MatchString(id) {
		return Snapshot{}, os.ErrNotExist
	}
	f, e := os.Open(filepath.Join(s.cfg.OutputDir, id))
	if e != nil {
		return Snapshot{}, e
	}
	defer f.Close()
	var snap Snapshot
	if e = json.NewDecoder(io.LimitReader(f, 32<<20)).Decode(&snap); e != nil || snap.Kind != "web_run" || snap.RunID != id {
		return Snapshot{}, os.ErrNotExist
	}
	return snap, nil
}
func (s *Server) snapshotHTTP(w http.ResponseWriter, id string) {
	snap, e := s.snapshot(id)
	if e != nil {
		fail(w, 404, "run not found")
		return
	}
	writeJSON(w, 200, snap)
}
func (s *Server) eventsHTTP(w http.ResponseWriter, req *http.Request, id string) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		fail(w, 500, "stream unavailable")
		return
	}
	s.mu.Lock()
	var snap Snapshot
	var ch chan Envelope
	if s.active != nil && s.active.snapshot.RunID == id {
		r := s.active
		snap = copySnapshot(r.snapshot)
		ch = make(chan Envelope, 64)
		r.subscribers[ch] = struct{}{}
		defer func() {
			s.mu.Lock()
			if _, ok := r.subscribers[ch]; ok {
				delete(r.subscribers, ch)
				close(ch)
			}
			s.mu.Unlock()
		}()
	}
	s.mu.Unlock()
	if ch == nil {
		var e error
		snap, e = s.snapshot(id)
		if e != nil {
			fail(w, 404, "run not found")
			return
		}
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache, no-transform")
	w.Header().Set("X-Accel-Buffering", "no")
	send := func(kind string, seq uint64, data any) bool {
		b, e := json.Marshal(data)
		if e != nil {
			return false
		}
		_, e = fmt.Fprintf(w, "id: %d\nevent: %s\ndata: %s\n\n", seq, kind, b)
		if e == nil {
			flusher.Flush()
		}
		return e == nil
	}
	if !send("snapshot", snap.Seq, snap) || ch == nil {
		return
	}
	ticker := time.NewTicker(20 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-req.Context().Done():
			return
		case e, open := <-ch:
			if !open {
				return
			}
			if !send("update", e.Seq, e) {
				return
			}
		case <-ticker.C:
			if _, e := io.WriteString(w, ": ping\n\n"); e != nil {
				return
			}
			flusher.Flush()
		}
	}
}
func (s *Server) traceHTTP(w http.ResponseWriter, r *http.Request, id string) {
	problem := ""
	values, filtered := r.URL.Query()["problem"]
	if filtered {
		if len(values) != 1 || values[0] == "" {
			fail(w, 400, "invalid problem")
			return
		}
		problem = values[0]
		if _, ok := s.problems[problem]; !ok {
			fail(w, 400, "invalid problem")
			return
		}
	}
	s.mu.Lock()
	path := filepath.Join(s.cfg.OutputDir, id+".trace.jsonl")
	if s.active != nil && s.active.snapshot.RunID == id {
		path = s.active.tmpTrace
	} else {
		tmp := filepath.Join(s.cfg.OutputDir, id+".trace.tmp")
		if _, err := os.Stat(tmp); err == nil {
			path = tmp
		}
	}
	f, e := os.Open(path)
	if os.IsNotExist(e) && strings.HasSuffix(path, ".trace.jsonl") {
		f, e = os.Open(filepath.Join(s.cfg.OutputDir, id+".trace.tmp"))
	}
	var size int64
	if e == nil {
		var info os.FileInfo
		info, e = f.Stat()
		if e == nil {
			size = info.Size()
		}
	}
	s.mu.Unlock()
	if e != nil {
		if f != nil {
			f.Close()
		}
		fail(w, 404, "trace not found")
		return
	}
	defer f.Close()
	w.Header().Set("Content-Type", "application/x-ndjson; charset=utf-8")
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=\"%s.trace.jsonl\"", id))
	reader := io.NewSectionReader(f, 0, size)
	if filtered {
		streamProblem(r.Context(), w, reader, problem)
		return
	}
	_, _ = io.Copy(w, reader)
}

// 2026-11-05: 대형 journal 전체를 메모리에 올리지 않고 선택된 문제의 이벤트만 전달한다.
func streamProblem(ctx context.Context, w io.Writer, reader io.Reader, problem string) {
	d := json.NewDecoder(reader)
	selected := false
	for {
		if ctx.Err() != nil {
			return
		}
		var raw json.RawMessage
		if err := d.Decode(&raw); err != nil {
			return
		}
		var header struct {
			Kind string `json:"kind"`
			Data struct {
				ID        string `json:"id"`
				ProblemID string `json:"problem_id"`
			} `json:"data"`
		}
		if json.Unmarshal(raw, &header) != nil {
			return
		}
		if header.Kind == "problem_started" {
			if selected {
				return
			}
			selected = header.Data.ID == problem
		}
		if header.Kind == "run_finished" {
			return
		}
		include := selected || (header.Kind == "problem_skipped" && header.Data.ProblemID == problem)
		if include {
			if _, err := w.Write(raw); err != nil {
				return
			}
			if _, err := io.WriteString(w, "\n"); err != nil {
				return
			}
			if header.Kind == "problem_skipped" || header.Kind == "problem_finished" {
				return
			}
		}
	}
}
func (s *Server) downloadHTTP(w http.ResponseWriter, id string) {
	snap, e := s.snapshot(id)
	if e != nil {
		fail(w, 404, "run not found")
		return
	}
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=\"%s.json\"", id))
	writeJSON(w, 200, snap)
}
func (s *Server) history(w http.ResponseWriter) {
	entries, e := os.ReadDir(s.cfg.OutputDir)
	if e != nil && !os.IsNotExist(e) {
		fail(w, 500, "history unavailable")
		return
	}
	type historyItem struct {
		RunID     string    `json:"run_id"`
		Status    string    `json:"status"`
		Model     string    `json:"model"`
		Problem   string    `json:"problem"`
		Total     int       `json:"total"`
		Completed int       `json:"completed"`
		StartedAt time.Time `json:"started_at"`
	}
	runs := make([]historyItem, 0)
	seen := map[string]bool{}
	for _, entry := range entries {
		if entry.IsDir() || !runIDPattern.MatchString(entry.Name()) {
			continue
		}
		snap, e := s.snapshot(entry.Name())
		if e == nil {
			runs = append(runs, historyItem{snap.RunID, snap.Status, snap.Model, snap.Problem, snap.Total, snap.Completed, snap.StartedAt})
			seen[snap.RunID] = true
		}
	}
	s.mu.Lock()
	for id, snap := range s.failed {
		if !seen[id] {
			runs = append(runs, historyItem{snap.RunID, snap.Status, snap.Model, snap.Problem, snap.Total, snap.Completed, snap.StartedAt})
		}
	}
	s.mu.Unlock()
	sort.Slice(runs, func(i, j int) bool { return runs[i].StartedAt.After(runs[j].StartedAt) })
	writeJSON(w, 200, map[string]any{"runs": runs})
}
