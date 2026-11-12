package desktop

import (
	"encoding/json"
	"fmt"
	"jevcancode/internal/application"
	"testing"
)

func traceSnapshot(t *testing.T, frame Frame) application.Snapshot {
	t.Helper()
	var s application.Snapshot
	if err := json.Unmarshal([]byte(frame.Text), &s); err != nil {
		t.Fatal(err)
	}
	return s
}

func TestReplayKeepsLastDecisionThroughTokenPhases(t *testing.T) {
	events := []TraceEvent{
		{Seq: 1, RunID: "r", Kind: "problem_started", Data: []byte(`{"id":"add"}`)},
		{Seq: 2, RunID: "r", Kind: "search", Data: []byte(`{"step":11,"id":"a","parent_id":"p","lexeme":"+","reason":"available"}`)},
		{Seq: 3, RunID: "r", Kind: "search", Data: []byte(`{"step":11,"reason":"decision_result","chosen_id":"a","decision_status":"received"}`)},
		{Seq: 4, RunID: "r", Kind: "search", Data: []byte(`{"reason":"phase_started"}`)},
		{Seq: 5, RunID: "r", Kind: "search", Data: []byte(`{"reason":"phase_finished"}`)},
		{Seq: 6, RunID: "r", Kind: "search", Data: []byte(`{"reason":"ended:failed"}`)},
		{Seq: 7, RunID: "r", Kind: "problem_started", Data: []byte(`{"id":"next"}`)},
		{Seq: 8, RunID: "r", Kind: "search", Data: []byte(`{"step":1,"id":"b","lexeme":"return","reason":"available"}`)},
	}
	frames := Reconstruct(events)
	if len(frames) != len(events) {
		t.Fatalf("raw frames=%d events=%d", len(frames), len(events))
	}
	for i, f := range frames {
		if f.Seq != events[i].Seq {
			t.Fatalf("sequence %d = %d", i, f.Seq)
		}
	}
	for _, i := range []int{3, 4, 5} {
		s := traceSnapshot(t, frames[i])
		if s.Step != 11 || s.ChosenID != "a" || len(s.Candidates) != 1 || s.Candidates[0].Lexeme != "+" || frames[i].Group != frames[2].Group {
			t.Errorf("frame %d: snapshot=%+v group=%q", i, s, frames[i].Group)
		}
	}
	reset := traceSnapshot(t, frames[6])
	if reset.Step != 0 || reset.ChosenID != "" || len(reset.Candidates) != 0 || frames[6].Group != "" {
		t.Fatalf("problem reset: %+v group=%q", reset, frames[6].Group)
	}
	next := traceSnapshot(t, frames[7])
	if next.Step != 1 || next.ChosenID != "" || len(next.Candidates) != 1 || frames[7].Group == frames[2].Group {
		t.Fatalf("next problem: %+v group=%q", next, frames[7].Group)
	}
	timeline := decisionTimeline(frames)
	if len(timeline) != 2 || timeline[0].Seq != 6 || timeline[1].Seq != 8 {
		t.Fatalf("timeline=%+v", timeline)
	}
}

func TestReplayComparisonRepeatedTrialAndLiveMerge(t *testing.T) {
	var events []TraceEvent
	add := func(trial int, event string) {
		events = append(events, TraceEvent{Seq: uint64(len(events) + 1), RunID: "r", Kind: "comparison", Data: []byte(fmt.Sprintf(`{"trial_index":%d,"trial":{"problem":"add","model":"Jev"},"event":%s}`, trial, event))})
	}
	add(0, `{"step":11,"id":"a","lexeme":"+","reason":"available"}`)
	add(0, `{"step":11,"reason":"decision_result","chosen_id":"a","decision_status":"received"}`)
	add(0, `{"reason":"phase_started"}`)
	add(0, `{"reason":"phase_finished"}`)
	add(0, `{"reason":"ended:failed"}`)
	add(1, `{"reason":"ended:cancelled"}`)
	add(2, `{"step":1,"id":"b","lexeme":"return","reason":"available"}`)
	frames := Reconstruct(events)
	for i := 2; i <= 4; i++ {
		s := traceSnapshot(t, frames[i])
		if s.Step != 11 || s.ChosenID != "a" || len(s.Candidates) != 1 || frames[i].Group != frames[1].Group {
			t.Errorf("trial frame %d: %+v group=%q", i, s, frames[i].Group)
		}
		merged := MergeSnapshot(frames[:i+1], application.Snapshot{RunID: "r", Seq: events[i].Seq, JobKind: "compare"})
		m := traceSnapshot(t, merged[len(merged)-1])
		if m.Step != s.Step || m.ChosenID != s.ChosenID || len(m.Candidates) != len(s.Candidates) || merged[len(merged)-1].Group != frames[i].Group {
			t.Errorf("merged=%+v replay=%+v", m, s)
		}
	}
	before := traceSnapshot(t, frames[5])
	if before.Step != 0 || before.ChosenID != "" || len(before.Candidates) != 0 || frames[5].Group != "" {
		t.Fatalf("cancelled before decision: %+v group=%q", before, frames[5].Group)
	}
	if s := traceSnapshot(t, frames[6]); s.Step != 1 || frames[6].Group == frames[1].Group || s.ChosenID != "" {
		t.Fatalf("repeated problem trial: %+v group=%q", s, frames[6].Group)
	}
	if timeline := decisionTimeline(frames); len(timeline) != 2 || timeline[0].Seq != 5 || timeline[1].Seq != 7 {
		t.Fatalf("timeline=%+v", timeline)
	}
}

func TestReplayBeforeDecisionDoesNotInventGroupOrChoice(t *testing.T) {
	for _, reason := range []string{"ended:failed", "ended:cancelled"} {
		t.Run(reason, func(t *testing.T) {
			events := []TraceEvent{
				{Seq: 1, RunID: "r", Kind: "problem_started", Data: []byte(`{"id":"add"}`)},
				{Seq: 2, RunID: "r", Kind: "search", Data: []byte(fmt.Sprintf(`{"reason":%q}`, reason))},
			}
			frames := Reconstruct(events)
			s := traceSnapshot(t, frames[1])
			if s.Step != 0 || s.ChosenID != "" || len(s.Candidates) != 0 || frames[1].Group != "" || len(decisionTimeline(frames)) != 0 {
				t.Fatalf("snapshot=%+v group=%q", s, frames[1].Group)
			}
		})
	}
}

func TestReplayASTStepZeroStillUpdatesStep(t *testing.T) {
	events := []TraceEvent{
		{Seq: 1, RunID: "r", Kind: "search", Data: []byte(`{"id":"a","step":3,"elapsed_ms":1}`)},
		{Seq: 2, RunID: "r", Kind: "search", Data: []byte(`{"id":"b","step":0,"elapsed_ms":2}`)},
	}
	frames := Reconstruct(events)
	if s := traceSnapshot(t, frames[1]); s.Step != 0 || frames[1].Group != "0:0" {
		t.Fatalf("AST step semantics changed: %+v group=%q", s, frames[1].Group)
	}
}
