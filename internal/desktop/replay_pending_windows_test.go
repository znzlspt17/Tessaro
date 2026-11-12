//go:build windows

package desktop

import (
	"context"
	"testing"
)

func TestPlaybackWaitsForPendingSelection(t *testing.T) {
	frames := []Frame{{RunID: "r", Seq: 1}, {RunID: "r", Seq: 2}}
	w := &window{playing: true, framePending: true, shownFrames: frames}
	for _, frame := range frames {
		w.replay.Add(frame)
	}
	if !w.replay.Select(1) {
		t.Fatal("cannot select first event")
	}
	w.moveReplay(1)
	if got := w.replay.View().Seq; got != 1 {
		t.Fatalf("advanced to %d while previous frame was loading", got)
	}
}

func TestCancelFrameAndTraceLoadStopsWork(t *testing.T) {
	frameCtx, frameCancel := context.WithCancel(context.Background())
	loadCtx, loadCancel := context.WithCancel(context.Background())
	w := &window{framePending: true, frameCancel: frameCancel, loadCancel: loadCancel}
	w.cancelFrameRequest()
	w.cancelTraceLoad()
	if frameCtx.Err() == nil || loadCtx.Err() == nil || w.framePending {
		t.Fatal("work still active after cancellation")
	}
}
