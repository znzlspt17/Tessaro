package desktop

import (
	"context"
	"fmt"
	"tessaro/internal/application"
	"reflect"
	"strings"
	"testing"
)

func TestTraceIndexRetainsEventsWithoutSnapshotPerEvent(t *testing.T) {
	events := make([]TraceEvent, 0, 4001)
	events = append(events, TraceEvent{Seq: 1, RunID: "r", Kind: "problem_started", Data: []byte(`{"id":"p"}`)})
	for n := 0; n < 4000; n++ {
		data := fmt.Sprintf(`{"step":%d,"id":"%d","parent_id":"root","lexeme":%q,"reason":"available"}`, n/40+1, n, strings.Repeat("x", 80))
		events = append(events, TraceEvent{Seq: uint64(n + 2), RunID: "r", Kind: "search", Data: []byte(data)})
	}
	var index traceIndex
	index.appendEvents(events[:2000])
	index.appendEvents(events[2000:])
	frames := index.framesView()
	if len(frames) != len(events) {
		t.Fatalf("raw replay lost events: got %d, want %d", len(frames), len(events))
	}
	var total int
	for _, f := range frames {
		total += len(f.Text)
	}
	if total > 256<<10 {
		t.Fatalf("retained snapshot text = %d bytes for %d events", total, len(events))
	}
}

func TestTraceIndexCancelledSelectionStopsBeforeReconstruction(t *testing.T) {
	var index traceIndex
	index.appendEvents([]TraceEvent{{Seq: 1, RunID: "r", Kind: "problem_started", Data: []byte(`{"id":"p"}`)}})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if got := index.frameAtContext(ctx, 1); got.Seq != 0 {
		t.Fatalf("cancelled selection produced frame: %+v", got)
	}
}

func TestReplayFinalFrameCanFollowTrace(t *testing.T) {
	final := application.Snapshot{RunID: "r", Seq: 5, Status: "completed"}
	got := mergeReplayFinal(Frame{}, &final, 5)
	if got.Seq != 5 || !strings.Contains(got.Text, `"status": "completed"`) {
		t.Fatalf("missing final frame: %+v", got)
	}
}

func TestTraceIndexReconstructsSelectedFrames(t *testing.T) {
	events := []TraceEvent{
		{Seq: 1, RunID: "r", Kind: "problem_started", Data: []byte(`{"id":"p"}`)},
		{Seq: 2, RunID: "r", Kind: "search", Data: []byte(`{"id":"x","parent_id":"root","lexeme":"x","reason":"forced","step":1}`)},
		{Seq: 3, RunID: "r", Kind: "search", Data: []byte(`{"reason":"frontier","frontier":[{"id":"x","rank":1}],"step":1}`)},
		{Seq: 4, RunID: "r", Kind: "problem_started", Data: []byte(`{"id":"q"}`)},
		{Seq: 5, RunID: "r", Kind: "search", Data: []byte(`{"id":"b","lexeme":"+","reason":"available","step":2}`)},
		{Seq: 6, RunID: "r", Kind: "search", Data: []byte(`{"reason":"decision_result","chosen_id":"b","decision_status":"received","step":2}`)},
	}
	want := Reconstruct(events)
	var index traceIndex
	index.appendEvents(events[:3])
	index.appendEvents(events[3:])
	for _, expected := range want {
		got := index.frameAt(expected.Seq)
		if !reflect.DeepEqual(got, expected) {
			t.Fatalf("sequence %d: got %+v, want %+v", expected.Seq, got, expected)
		}
	}
	if got := index.frameAt(42); got.Seq != 0 {
		t.Fatalf("missing sequence should not invent a frame: %+v", got)
	}
}
