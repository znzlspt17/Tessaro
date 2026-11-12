package rank

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os/exec"
	"sync"
	"time"

	"jevcancode/internal/process"
)

var errWorkerUnavailable = errors.New("worker_unavailable")
var errWorkerProtocol = errors.New("worker_protocol_error")
var errWorkerInference = errors.New("worker_inference_failed")
var errWorkerInitialization = errors.New("worker_initialization_failed")

const maxWorkerFrame = 1 << 20

type WorkerConfig struct {
	Python, Script, ModelDir, Model, Revision, Runtime string
	StartupTimeout, RequestTimeout                     time.Duration
}
type workerFrame struct {
	Version int             `json:"version"`
	Type    string          `json:"type"`
	ID      uint64          `json:"id,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
}
type workerRead struct {
	data []byte
	err  error
}
type Worker struct {
	config        WorkerConfig
	lock          chan struct{}
	metadataMu    sync.Mutex
	returnedModel string
	cmd           *exec.Cmd
	group         *process.Group
	stdin         io.WriteCloser
	stdout        io.ReadCloser
	frames        chan workerRead
	done          chan struct{}
	nextID        uint64
	closed        bool
}

// 2026-11-12: worker 프로토콜은 Choice의 정규화된 분포를 전달한다.
func (*Worker) NormalizedCategorical() bool { return true }
func (*Worker) HTTPRequests() int64         { return 0 }

func NewWorker(c WorkerConfig) *Worker {
	if c.StartupTimeout <= 0 {
		c.StartupTimeout = 180 * time.Second
	}
	if c.RequestTimeout <= 0 {
		c.RequestTimeout = 60 * time.Second
	}
	if c.Model == "" {
		c.Model = "Mapika/decider-4b"
	}
	w := &Worker{config: c, lock: make(chan struct{}, 1)}
	w.lock <- struct{}{}
	return w
}
func (w *Worker) acquire(ctx context.Context) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-w.lock:
		return nil
	}
}
func (w *Worker) release() { w.lock <- struct{}{} }

// 2026-11-07: 모델 초기화 시간을 문제별 실행 예산에 포함하지 않도록 명시적으로 준비한다.
func (w *Worker) Prepare(ctx context.Context) error {
	if err := w.acquire(ctx); err != nil {
		return err
	}
	defer w.release()
	return w.prepareLocked(ctx)
}
func (w *Worker) prepareLocked(ctx context.Context) error {
	if w.closed {
		return errWorkerUnavailable
	}
	if w.cmd != nil {
		return nil
	}
	c := w.config
	if c.Python == "" || c.Script == "" || c.ModelDir == "" {
		return errWorkerUnavailable
	}
	cmd := exec.Command(c.Python, "-u", c.Script, "--model-dir", c.ModelDir)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return errWorkerUnavailable
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		_ = stdin.Close()
		return errWorkerUnavailable
	}
	cmd.Stderr = io.Discard
	group, err := process.Start(cmd)
	if err != nil {
		_ = stdin.Close()
		_ = stdout.Close()
		return errWorkerUnavailable
	}
	w.cmd, w.group, w.stdin, w.stdout = cmd, group, stdin, stdout
	w.frames = make(chan workerRead, 1)
	w.done = make(chan struct{})
	frames := w.frames
	go readWorkerFrames(stdout, frames, w.done)
	startCtx, cancel := context.WithTimeout(ctx, c.StartupTimeout)
	defer cancel()
	frame, err := w.receive(startCtx)
	if err != nil {
		w.stopLocked()
		return err
	}
	if frame.Type == "error" && frame.ID == 0 {
		w.stopLocked()
		return errWorkerInitialization
	}
	if frame.Type != "ready" || frame.ID != 0 {
		w.stopLocked()
		return errWorkerProtocol
	}
	return nil
}

// 2026-11-07: 개행이 없는 부분 프레임과 1MiB 초과 입력을 수락하지 않는다.
func readWorkerFrames(r io.Reader, frames chan<- workerRead, done <-chan struct{}) {
	reader := bufio.NewReaderSize(r, 4096)
	send := func(v workerRead) bool {
		select {
		case frames <- v:
			return true
		case <-done:
			return false
		}
	}
	for {
		var frame []byte
		for {
			fragment, err := reader.ReadSlice('\n')
			if len(frame)+len(fragment) > maxWorkerFrame+1 {
				send(workerRead{err: errWorkerProtocol})
				return
			}
			frame = append(frame, fragment...)
			if err == bufio.ErrBufferFull {
				continue
			}
			if err != nil {
				if len(frame) > 0 {
					send(workerRead{err: errWorkerProtocol})
				} else {
					send(workerRead{err: errWorkerUnavailable})
				}
				return
			}
			if !send(workerRead{data: frame[:len(frame)-1]}) {
				return
			}
			break
		}
	}
}

func (w *Worker) receive(ctx context.Context) (workerFrame, error) {
	var f workerFrame
	select {
	case <-ctx.Done():
		return f, ctx.Err()
	case raw := <-w.frames:
		if raw.err != nil {
			return f, raw.err
		}
		if len(raw.data) > maxWorkerFrame || rejectDuplicateKeys(raw.data) != nil || json.Unmarshal(raw.data, &f) != nil || f.Version != 1 {
			return f, errWorkerProtocol
		}
		return f, nil
	}
}
func (w *Worker) Rank(ctx context.Context, state string, cs []Candidate) ([]Score, error) {
	s, _, e := w.RankWithUsage(ctx, state, cs)
	return s, e
}
func (w *Worker) RankWithUsage(ctx context.Context, state string, cs []Candidate) ([]Score, *Usage, error) {
	d, err := w.RankDecision(ctx, state, cs)
	return d.Scores, d.Usage, err
}
func (w *Worker) RankDecision(ctx context.Context, state string, cs []Candidate) (Decision, error) {
	var d Decision
	s, u, err := w.rankDecision(ctx, state, cs, &d.ChosenID)
	d.Scores, d.Usage = s, u
	return d, err
}
func (w *Worker) rankDecision(ctx context.Context, state string, cs []Candidate, chosen *string) ([]Score, *Usage, error) {
	payload, criteria, err := choiceRequest(w.config.Model, state, cs)
	if err != nil {
		return nil, nil, err
	}
	if err = w.acquire(ctx); err != nil {
		return nil, nil, err
	}
	defer w.release()
	if err = w.prepareLocked(ctx); err != nil {
		return nil, nil, err
	}
	w.metadataMu.Lock()
	w.returnedModel = ""
	w.metadataMu.Unlock()
	w.nextID++
	request := struct {
		Version int             `json:"version"`
		Type    string          `json:"type"`
		ID      uint64          `json:"id"`
		Payload json.RawMessage `json:"payload"`
	}{1, "rank", w.nextID, payload}
	data, err := json.Marshal(request)
	if err != nil || len(data) > maxWorkerFrame {
		return nil, nil, errWorkerProtocol
	}
	data = append(data, '\n')
	requestCtx, cancel := context.WithTimeout(ctx, w.config.RequestTimeout)
	defer cancel()
	writeDone := make(chan error, 1)
	stdin := w.stdin
	go func() {
		n, e := stdin.Write(data)
		if e == nil && n != len(data) {
			e = io.ErrShortWrite
		}
		writeDone <- e
	}()
	select {
	case <-requestCtx.Done():
		w.stopLocked()
		return nil, nil, requestCtx.Err()
	case e := <-writeDone:
		if e != nil {
			w.stopLocked()
			return nil, nil, errWorkerUnavailable
		}
	}
	frame, err := w.receive(requestCtx)
	if err != nil {
		w.stopLocked()
		return nil, nil, err
	}
	if frame.ID != w.nextID {
		w.stopLocked()
		return nil, nil, errWorkerProtocol
	}
	if frame.Type == "error" {
		w.stopLocked()
		return nil, nil, errWorkerInference
	}
	if frame.Type != "result" || len(frame.Result) == 0 || bytes.Equal(frame.Result, []byte("null")) {
		w.stopLocked()
		return nil, nil, errWorkerProtocol
	}
	scores, model, usage, err := choiceResponse(frame.Result, criteria, cs, chosen)
	if err != nil {
		w.stopLocked()
		return nil, nil, errWorkerProtocol
	}
	w.metadataMu.Lock()
	w.returnedModel = model
	w.metadataMu.Unlock()
	return scores, usage, nil
}
func (w *Worker) Metadata() Metadata {
	w.metadataMu.Lock()
	defer w.metadataMu.Unlock()
	return Metadata{Model: w.config.Model, ReturnedModel: w.returnedModel, Revision: w.config.Revision, Runtime: w.config.Runtime, ScoreSemantics: "choice probability (higher ranks first)"}
}
func (w *Worker) stopLocked() {
	if w.cmd == nil {
		return
	}
	close(w.done)
	_ = w.stdin.Close()
	_ = w.group.Close()
	_ = w.cmd.Wait()
	_ = w.stdout.Close()
	w.cmd = nil
	w.group = nil
	w.stdin = nil
	w.stdout = nil
	w.frames = nil
}
func (w *Worker) Close() error {
	if err := w.acquire(context.Background()); err != nil {
		return err
	}
	defer w.release()
	w.closed = true
	w.stopLocked()
	return nil
}
