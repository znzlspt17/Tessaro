package desktop

import (
	"encoding/json"
	"jevcancode/internal/application"
	"strings"
	"testing"
)

func TestReplayPauseAndResume(t *testing.T) {
	var r Replay
	r.Add(Frame{RunID: "a", Seq: 1, Text: "one"})
	r.Pause()
	r.Add(Frame{RunID: "a", Seq: 2, Text: "two"})
	if got := r.View(); got.Text != "one" {
		t.Fatalf("paused view = %+v", got)
	}
	r.Resume()
	if got := r.View(); got.Seq != 2 {
		t.Fatalf("live view = %+v", got)
	}
	if !r.Select(1) || r.View().Text != "one" {
		t.Fatal("selection failed")
	}
}
func TestReplayRejectsMismatchedOrDuplicateRecords(t *testing.T) {
	var r Replay
	if !r.Add(Frame{RunID: "a", Seq: 2}) || r.Add(Frame{RunID: "b", Seq: 3}) || r.Add(Frame{RunID: "a", Seq: 2}) {
		t.Fatal("accepted inconsistent record")
	}
}
func TestFilterCandidates(t *testing.T) {
	got := FilterCandidates([]Candidate{{Text: "alpha", Reason: "accepted"}, {Text: "beta", Reason: "rejected"}}, "BETA")
	if len(got) != 1 || got[0].Text != "beta" {
		t.Fatalf("got %+v", got)
	}
}

func TestTraceReconstructionKeepsProblemBoundaries(t *testing.T) {
	events := []TraceEvent{{Seq: 1, RunID: "a", Kind: "problem_started", Data: []byte(`{"id":"p1"}`)}, {Seq: 2, RunID: "a", Kind: "search", Data: []byte(`{"id":"n1","parent_id":"root","lexeme":"x","reason":"available","step":1}`)}, {Seq: 3, RunID: "a", Kind: "search", Data: []byte(`{"reason":"frontier","frontier":[{"id":"n1","rank":1}],"step":1}`)}, {Seq: 4, RunID: "a", Kind: "problem_started", Data: []byte(`{"id":"p2"}`)}}
	frames := Reconstruct(events)
	if len(frames) != 4 || !strings.Contains(frames[2].Text, `"code": "x"`) || strings.Contains(frames[3].Text, `"code": "x"`) {
		t.Fatalf("frames: %+v", frames)
	}
}

func TestTraceReconstructionASTAndReport(t *testing.T) {
	events := []TraceEvent{{Seq: 1, RunID: "a", Kind: "search", Data: []byte(`{"id":"n","reason":"ranked","rank":1,"step":1,"code":"func f() {}","elapsed_ms":1}`)}, {Seq: 2, RunID: "a", Kind: "report", Data: []byte(`{"total":3}`)}}
	frames := Reconstruct(events)
	if len(frames) != 2 || !strings.Contains(frames[0].Text, `func f()`) || !strings.Contains(frames[1].Text, `"total": 3`) {
		t.Fatalf("frames: %+v", frames)
	}
}

func TestComparisonReplaySeparatesTrials(t *testing.T) {
	events := []TraceEvent{{Seq: 1, RunID: "a", Kind: "comparison", Data: []byte(`{"trial_index":0,"trial":{"problem":"p1"},"event":{"id":"n","reason":"ranked","rank":1,"step":1,"code":"first","elapsed_ms":1}}`)}, {Seq: 2, RunID: "a", Kind: "comparison", Data: []byte(`{"trial_index":1,"trial":{"problem":"p2"},"event":{"id":"x","reason":"ranked","rank":1,"step":1,"code":"second","elapsed_ms":1}}`)}}
	frames := Reconstruct(events)
	if len(frames) != 2 || !strings.Contains(frames[0].Text, "first") || !strings.Contains(frames[1].Text, "second") || strings.Contains(frames[1].Text, "first") || !strings.Contains(frames[1].Text, "p2") {
		t.Fatalf("frames: %+v", frames)
	}
}

func TestHistoryRequestGuardRejectsStale(t *testing.T) {
	var g HistoryGuard
	a := g.Begin("A", "old")
	b := g.Begin("B", "old")
	if g.Accept(a, "A", "old") || !g.Accept(b, "B", "old") || g.Accept(b, "B", "new") || g.Accept(b, "A", "old") {
		t.Fatal("stale history request accepted")
	}
}

func TestParentSelectionAndContext(t *testing.T) {
	ids := []string{"b", "a"}
	if ParentIndex(ids, "a") != 2 || ParentIndex(ids, "gone") != 0 {
		t.Fatal("parent selection did not follow ID")
	}
	ctx := map[string]any{"intent": "i", "parents": map[string]any{"a": "A", "b": "B"}}
	filtered := FilterContext(ctx, "a")
	parents := filtered["parents"].(map[string]any)
	if len(parents) != 1 || parents["a"] != "A" || len(ctx["parents"].(map[string]any)) != 2 {
		t.Fatal("context filter failed")
	}
}

func TestReplayRejectionReasonAndUnknownLabel(t *testing.T) {
	events := []TraceEvent{{Seq: 1, RunID: "a", Kind: "search", Data: []byte(`{"id":"n","reason":"declaration_rejected","rejection_reason":"return_type","step":1}`)}, {Seq: 2, RunID: "a", Kind: "search", Data: []byte(`{"id":"n","reason":"body_rejected","rejection_reason":"unused_expression","step":1}`)}, {Seq: 3, RunID: "a", Kind: "problem_started", Data: []byte(`{"id":"p2"}`)}}
	frames := Reconstruct(events)
	if !strings.Contains(frames[0].Text, "return_type") || !strings.Contains(frames[1].Text, "unused_expression") || strings.Contains(frames[2].Text, "unused_expression") || RejectionLabel("unknown_untrusted") == "unknown_untrusted" {
		t.Fatalf("frames: %+v", frames)
	}
}

func TestTraceTokenMarkerUsesKnownFinalLexeme(t *testing.T) {
	events := []TraceEvent{{Seq: 1, RunID: "a", Kind: "search", Data: []byte(`{"id":"n","parent_id":"root","lexeme":"값","reason":"forced","step":1}`)}, {Seq: 2, RunID: "a", Kind: "search", Data: []byte(`{"reason":"frontier","frontier":[{"id":"n","rank":1}],"step":1}`)}}
	frames := Reconstruct(events)
	m, ok := frames[1].Markers["n"]
	if !ok || m.Start != 0 || m.End != 1 || m.Reason != "forced" {
		t.Fatalf("marker=%+v ok=%v", m, ok)
	}
}

func TestHistoryListGuardRejectsOlderRefreshOrManager(t *testing.T) {
	var g HistoryGuard
	old := g.Begin("list", "manager1")
	current := g.Begin("list", "manager1")
	if g.Accept(old, "list", "manager1") || g.Accept(current, "list", "manager2") || !g.Accept(current, "list", "manager1") {
		t.Fatal("history list accepted stale result")
	}
}

func TestMergeFinalSnapshotReplacesSummaryAndPreservesMarker(t *testing.T) {
	frames := []Frame{{RunID: "r", Seq: 3, Text: `{"payload":{"job_kind":"compare","total":1}}`, Markers: map[string]Marker{"n": {Start: 0, End: 1, Reason: "forced"}}}}
	s := application.Snapshot{RunID: "r", Seq: 3, Payload: json.RawMessage(`{"groups":[{"model":"real"}]}`)}
	got := MergeSnapshot(frames, s)
	if len(got) != 1 || !strings.Contains(got[0].Text, `"real"`) || got[0].Markers["n"].Reason != "forced" {
		t.Fatalf("got %+v", got)
	}
	s.Seq = 4
	got = MergeSnapshot(got, s)
	if len(got) != 2 || got[1].Seq != 4 {
		t.Fatalf("got %+v", got)
	}
	s.RunID = "other"
	if len(MergeSnapshot(got, s)) != 2 {
		t.Fatal("accepted mismatched run")
	}
}

func TestReportSummaryIsNotPresentedAsCompletePayload(t *testing.T) {
	frames := Reconstruct([]TraceEvent{{Seq: 1, RunID: "r", Kind: "report", Data: []byte(`{"job_kind":"compare","total":1}`)}})
	if strings.Contains(frames[0].Text, `"payload"`) {
		t.Fatal("summary exposed as full report")
	}
	var r Replay
	r.Add(frames[0])
	r.Pause()
	r.Merge(MergeSnapshot(frames, application.Snapshot{RunID: "r", Seq: 1, Payload: json.RawMessage(`{"groups":["complete"]}`)})[0])
	if !strings.Contains(r.View().Text, "complete") {
		t.Fatal("paused final frame not updated")
	}
}

func TestFilterContextLimitsSentCriteriaToSelectedParent(t *testing.T) {
	ctx := map[string]any{"criteria": map[string]any{"one": map[string]any{"parent_id": "a", "lexeme": "("}, "two": map[string]any{"parent_id": "b", "lexeme": "+"}}}
	got := FilterContext(ctx, "a")["criteria"].(map[string]any)
	if len(got) != 1 || got["one"] == nil || len(ctx["criteria"].(map[string]any)) != 2 {
		t.Fatalf("filtered criteria = %#v", got)
	}
}

func TestReplayPreservesRejectedCandidatesBeforeAvailable(t *testing.T) {
	events := []TraceEvent{
		{Seq: 1, RunID: "a", Kind: "search", Data: []byte(`{"id":"r1","parent_id":"p","lexeme":"(","token_kind":"(","reason":"body_rejected","rejection_reason":"non_callable","step":2}`)},
		{Seq: 2, RunID: "a", Kind: "search", Data: []byte(`{"id":"c1","parent_id":"p","lexeme":"+","token_kind":"+","reason":"available","step":2}`)},
		{Seq: 3, RunID: "a", Kind: "search", Data: []byte(`{"reason":"decision_context","step":2,"decision_context":{"criteria":{"c1":{"parent_id":"p","lexeme":"+"}}}}`)},
		{Seq: 4, RunID: "a", Kind: "search", Data: []byte(`{"id":"r2","parent_id":"q","lexeme":"(","token_kind":"(","reason":"declaration_rejected","rejection_reason":"return_type","step":3}`)},
	}
	frames := Reconstruct(events)
	var snapshot map[string]any
	if err := json.Unmarshal([]byte(frames[2].Text), &snapshot); err != nil {
		t.Fatal(err)
	}
	candidates := snapshot["candidates"].([]any)
	if len(candidates) != 2 {
		t.Fatalf("same-step candidates: %#v", candidates)
	}
	rejected := candidates[0].(map[string]any)
	if rejected["id"] != "r1" || rejected["parent_id"] != "p" || rejected["lexeme"] != "(" || rejected["rejection_reason"] != "non_callable" {
		t.Fatalf("rejected: %#v", rejected)
	}
	snapshot = nil
	if err := json.Unmarshal([]byte(frames[3].Text), &snapshot); err != nil {
		t.Fatal(err)
	}
	candidates = snapshot["candidates"].([]any)
	if len(candidates) != 1 || candidates[0].(map[string]any)["id"] != "r2" {
		t.Fatalf("next-step candidates: %#v", candidates)
	}
	if snapshot["decision_context"] != nil {
		t.Fatalf("old decision context retained: %#v", snapshot["decision_context"])
	}
}

func TestHypothesisLiveFramesStayBounded(t *testing.T) {
	frames := make([]Frame, 10000)
	for i := range frames {
		frames[i] = Frame{RunID: "h", Seq: uint64(i + 1), Text: `{"kind":"hypothesis_detail"}`}
	}
	s := application.Snapshot{RunID: "h", Seq: 10001, JobKind: "hypothesis", Status: "running"}
	got := MergeSnapshot(frames, s)
	if len(got) != 1 || got[0].Seq != s.Seq {
		t.Fatalf("got %d frames; want only the current snapshot", len(got))
	}
}
