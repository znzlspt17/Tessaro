package desktop

import (
	"context"
	"sync"
)

// 2026-11-08: 원본 이벤트와 작은 목록 정보만 보관해 이벤트마다 전체 상태가 복제되지 않게 한다.
type traceIndex struct {
	mu         sync.Mutex
	events     []TraceEvent
	frames     []Frame
	reducer    *traceReducer
	replayGate chan struct{}
}

func (i *traceIndex) appendEvents(events []TraceEvent) []Frame {
	i.mu.Lock()
	defer i.mu.Unlock()
	if i.reducer == nil {
		i.reducer = newTraceReducer()
	}
	var added []Frame
	for _, e := range events {
		f, ok := i.reducer.apply(e, false)
		if !ok {
			continue
		}
		i.events = append(i.events, e)
		i.frames = append(i.frames, f)
		added = append(added, f)
	}
	return added
}

func (i *traceIndex) framesView() []Frame {
	i.mu.Lock()
	defer i.mu.Unlock()
	return append([]Frame(nil), i.frames...)
}

func (i *traceIndex) currentFrame() Frame {
	i.mu.Lock()
	defer i.mu.Unlock()
	if i.reducer == nil {
		return Frame{}
	}
	return i.reducer.frame(true)
}

// 2026-11-08: 재생 선택은 작업 스레드에서만 해당 위치까지 다시 적용한다.
func (i *traceIndex) frameAt(seq uint64) Frame {
	return i.frameAtContext(context.Background(), seq)
}

func (i *traceIndex) frameAtContext(ctx context.Context, seq uint64) Frame {
	i.mu.Lock()
	if i.replayGate == nil {
		i.replayGate = make(chan struct{}, 1)
	}
	gate := i.replayGate
	i.mu.Unlock()
	select {
	case <-ctx.Done():
		return Frame{}
	case gate <- struct{}{}:
	}
	defer func() { <-gate }()
	i.mu.Lock()
	if len(i.events) == 0 || seq > i.events[len(i.events)-1].Seq {
		i.mu.Unlock()
		return Frame{}
	}
	events := append([]TraceEvent(nil), i.events...)
	i.mu.Unlock()
	r := newTraceReducer()
	for n, e := range events {
		if n%512 == 0 {
			select {
			case <-ctx.Done():
				return Frame{}
			default:
			}
		}
		if e.Seq > seq {
			break
		}
		f, ok := r.apply(e, e.Seq == seq)
		if ok && e.Seq == seq {
			return f
		}
	}
	return Frame{}
}
