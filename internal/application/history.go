package application

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"time"
)

type Record struct {
	ID        string          `json:"id"`
	Kind      string          `json:"kind"`
	Status    string          `json:"status"`
	Data      json.RawMessage `json:"data"`
	Trace     bool            `json:"trace"`
	StartedAt time.Time       `json:"started_at"`
}

var runID = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}\.\d{3}$`)

// 2026-11-07: 이전 웹·CLI 결과를 원본 변경 없이 구분하고 불완전 기록도 보존한다.
func (m *Manager) ReadHistory(id string) (Record, error) {
	if !runID.MatchString(id) {
		return Record{}, os.ErrNotExist
	}
	m.mu.Lock()
	failed, exists := m.failed[id]
	m.mu.Unlock()
	if exists {
		b, err := json.Marshal(failed)
		if err != nil {
			return Record{}, errors.New("invalid_record")
		}
		trace := false
		for _, suffix := range []string{".trace.jsonl", ".trace.tmp"} {
			if _, e := os.Stat(filepath.Join(m.cfg.OutputDir, id+suffix)); e == nil {
				trace = true
				break
			}
		}
		return Record{ID: id, Kind: failed.Kind, Status: failed.Status, Data: b, Trace: trace, StartedAt: failed.StartedAt}, nil
	}
	f, e := os.Open(filepath.Join(m.cfg.OutputDir, id))
	if e != nil {
		return Record{}, e
	}
	defer f.Close()
	b, e := io.ReadAll(io.LimitReader(f, (32<<20)+1))
	if e != nil || len(b) > 32<<20 {
		return Record{}, errors.New("invalid_record")
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(b, &fields) != nil || fields == nil {
		return Record{}, errors.New("invalid_record")
	}
	kind := "unknown"
	status := ""
	_ = json.Unmarshal(fields["kind"], &kind)
	_ = json.Unmarshal(fields["status"], &status)
	if kind == "" {
		kind = "unknown"
	}
	if kind == "unknown" {
		switch {
		case fields["groups"] != nil && fields["outcomes"] != nil:
			kind = "compare"
		case fields["evaluation_kind"] != nil && fields["items"] != nil:
			kind = "benchmark"
		case fields["engine"] != nil && fields["candidate_unit"] != nil:
			kind = "synthesis"
		}
	}
	if kind == "web_run" || kind == "desktop_run" {
		var storedID string
		_ = json.Unmarshal(fields["run_id"], &storedID)
		if storedID != id {
			return Record{}, errors.New("invalid_record")
		}
	}
	var started time.Time
	_ = json.Unmarshal(fields["started_at"], &started)
	if started.IsZero() {
		if info, err := f.Stat(); err == nil {
			started = info.ModTime()
		}
	}
	m.mu.Lock()
	active := m.cancel != nil && m.snapshot.RunID == id
	if active {
		b, _ = json.Marshal(m.snapshot)
		status = m.snapshot.Status
		started = m.snapshot.StartedAt
	}
	m.mu.Unlock()
	if !active && (status == "running" || status == "cancelling") {
		status = "interrupted"
	}
	trace := false
	for _, suffix := range []string{".trace.jsonl", ".trace.tmp"} {
		if _, err := os.Stat(filepath.Join(m.cfg.OutputDir, id+suffix)); err == nil {
			trace = true
			break
		}
	}
	if !active && trace && (kind == "web_run" || kind == "desktop_run") && status == "completed" {
		events, err := m.ReadTrace(id)
		if err != nil {
			return Record{}, err
		}
		terminal := false
		for _, event := range events {
			if event.Kind == "run_finished" || event.Kind == "storage_error" {
				terminal = true
				break
			}
		}
		if !terminal {
			status = "interrupted"
		}
	}
	return Record{ID: id, Kind: kind, Status: status, Data: append(json.RawMessage(nil), b...), Trace: trace, StartedAt: started}, nil
}
func (m *Manager) History() ([]Record, error) {
	entries, err := os.ReadDir(m.cfg.OutputDir)
	if os.IsNotExist(err) {
		return []Record{}, nil
	}
	if err != nil {
		return nil, err
	}
	out := []Record{}
	for _, entry := range entries {
		if entry.IsDir() || !runID.MatchString(entry.Name()) {
			continue
		}
		rec, e := m.ReadHistory(entry.Name())
		if e == nil {
			out = append(out, rec)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].StartedAt.Equal(out[j].StartedAt) {
			return out[i].ID > out[j].ID
		}
		return out[i].StartedAt.After(out[j].StartedAt)
	})
	return out, nil
}

// ReadTrace는 저장 중인 journal의 완성된 줄만 읽으며 마지막 부분 줄을 제외한다.
func (m *Manager) ReadTrace(id string) ([]Envelope, error) {
	if !runID.MatchString(id) {
		return nil, os.ErrNotExist
	}
	var f *os.File
	var err error
	for _, suffix := range []string{".trace.jsonl", ".trace.tmp"} {
		f, err = os.Open(filepath.Join(m.cfg.OutputDir, id+suffix))
		if err == nil {
			break
		}
		if !os.IsNotExist(err) {
			return nil, err
		}
	}
	if f == nil {
		return nil, os.ErrNotExist
	}
	defer f.Close()
	r := bufio.NewReaderSize(f, 64<<10)
	out := []Envelope{}
	line := 0
	for {
		var b []byte
		var e error
		for {
			var part []byte
			part, e = r.ReadSlice('\n')
			if len(b)+len(part) > 8<<20 {
				return nil, fmt.Errorf("invalid trace line %d", line+1)
			}
			b = append(b, part...)
			if e != bufio.ErrBufferFull {
				break
			}
		}
		line++
		if e == io.EOF {
			break
		}
		if e != nil {
			return nil, e
		}
		if len(b) > 8<<20 {
			return nil, fmt.Errorf("invalid trace line %d", line)
		}
		b = bytes.TrimSpace(b)
		if len(b) == 0 {
			continue
		}
		var event Envelope
		if json.Unmarshal(b, &event) != nil {
			return nil, fmt.Errorf("invalid trace line %d", line)
		}
		if event.Kind == "" {
			event.Kind = "search"
			var legacy map[string]json.RawMessage
			_ = json.Unmarshal(b, &legacy)
			if legacy["trial_index"] != nil && legacy["event"] != nil {
				event.Kind = "comparison"
			}
			event.Data = append(json.RawMessage(nil), b...)
			event.RunID = id
			event.Seq = uint64(len(out) + 1)
		}
		out = append(out, event)
	}
	return out, nil
}

// TraceCursor는 기록 파일의 완성된 행까지 읽은 위치와 레거시 이벤트의 순서를 보존한다.
type TraceCursor struct {
	Offset int64
	Events uint64
	Lines  uint64
}

// ReadTraceBatch는 완성된 행을 최대 limit개, 한 배치당 약 8 MiB까지 읽는다.
// 한 행은 최대 8 MiB이므로 완성 행 단위로 처리한 실제 크기는 그보다 클 수 있다.
// more는 한도에 도달해
// 추가 호출이 필요할 수 있음을 뜻하며, 다음 호출에서 빈 배치가 나올 수 있다.
// 2026-11-08: 전체 trace를 반복 로드하지 않아도 느린 화면이 이벤트를 빠짐없이 따라가게 한다.
func (m *Manager) ReadTraceBatch(id string, cursor TraceCursor, limit int) ([]Envelope, TraceCursor, bool, error) {
	// 2026-11-12: Windows에서 읽는 중 trace 이름 변경이 실패하지 않도록 마감과 직렬화한다.
	m.mu.Lock()
	defer m.mu.Unlock()
	if !runID.MatchString(id) {
		return nil, cursor, false, os.ErrNotExist
	}
	if cursor.Offset < 0 || limit <= 0 || limit > 4096 {
		return nil, cursor, false, errors.New("invalid trace cursor or batch size")
	}

	var f *os.File
	var err error
	// 최종 파일 확인 직후 임시 파일이 이름 변경되는 경우 최종 파일을 다시 확인한다.
	for _, suffix := range []string{".trace.jsonl", ".trace.tmp", ".trace.jsonl"} {
		f, err = os.Open(filepath.Join(m.cfg.OutputDir, id+suffix))
		if err == nil {
			break
		}
		if !os.IsNotExist(err) {
			return nil, cursor, false, err
		}
	}
	if f == nil {
		return nil, cursor, false, os.ErrNotExist
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return nil, cursor, false, err
	}
	if cursor.Offset > info.Size() {
		return nil, cursor, false, errors.New("trace shorter than cursor")
	}
	if _, err = f.Seek(cursor.Offset, io.SeekStart); err != nil {
		return nil, cursor, false, err
	}
	r := bufio.NewReaderSize(f, 64<<10)
	next := cursor
	out := make([]Envelope, 0, limit)
	for len(out) < limit {
		var b []byte
		var readErr error
		for {
			var part []byte
			part, readErr = r.ReadSlice('\n')
			if len(b)+len(part) > 8<<20 {
				return nil, cursor, false, fmt.Errorf("invalid trace line %d", next.Lines+1)
			}
			b = append(b, part...)
			if readErr != bufio.ErrBufferFull {
				break
			}
		}
		if readErr == io.EOF {
			return out, next, false, nil
		}
		if readErr != nil {
			return nil, cursor, false, readErr
		}
		next.Offset += int64(len(b))
		next.Lines++
		b = bytes.TrimSpace(b)
		if len(b) == 0 {
			if next.Offset-cursor.Offset >= 8<<20 {
				return out, next, true, nil
			}
			continue
		}
		var event Envelope
		if json.Unmarshal(b, &event) != nil {
			return nil, cursor, false, fmt.Errorf("invalid trace line %d", next.Lines)
		}
		if event.Kind == "" {
			event.Kind = "search"
			var legacy map[string]json.RawMessage
			_ = json.Unmarshal(b, &legacy)
			if legacy["trial_index"] != nil && legacy["event"] != nil {
				event.Kind = "comparison"
			}
			event.Data = append(json.RawMessage(nil), b...)
			event.RunID = id
			event.Seq = next.Events + 1
		}
		next.Events++
		out = append(out, event)
		if next.Offset-cursor.Offset >= 8<<20 {
			return out, next, true, nil
		}
	}
	return out, next, true, nil
}
