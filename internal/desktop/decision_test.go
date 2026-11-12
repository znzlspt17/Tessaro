package desktop

import (
	"encoding/json"
	"jevcancode/internal/application"
	"strconv"
	"strings"
	"testing"
)

func TestDecisionRowsKeepParentChoiceAndExclusions(t *testing.T) {
	score := 0.5
	s := application.Snapshot{Candidates: []application.CandidateItem{{ID: "a", ParentID: "p", Lexeme: "+", Reason: "ranked", Score: &score}, {ID: "b", ParentID: "q", Lexeme: "+", Reason: "active", Score: &score}, {ID: "x", ParentID: "p", Lexeme: ";", Reason: "body_rejected", RejectionReason: "unused_expression"}}, DecisionContext: map[string]any{"criteria": map[string]any{"a": map[string]any{"parent_id": "p"}, "b": map[string]any{"parent_id": "q"}}}}
	sent, excluded := decisionRows(s, "b", "", "")
	if len(sent) != 2 || len(excluded) != 1 || sent[0].ID != "b" || sent[0].ParentID != "q" {
		t.Fatalf("sent=%+v excluded=%+v", sent, excluded)
	}
}
func TestPreviewPreservesTokensAndMarksUnicode(t *testing.T) {
	text, start, end := previewCode("func f() { println ( \"한 글\" )", "값")
	if !strings.Contains(text, "\"한 글\"") || start >= end || end-start != 1 {
		t.Fatalf("preview=%q %d:%d", text, start, end)
	}
}
func TestDecisionSummaryDoesNotInventChoice(t *testing.T) {
	s := application.Snapshot{Candidates: []application.CandidateItem{{ID: "a", Lexeme: "+", Rank: 1}}}
	if got := decisionSummary(s, "", ""); !strings.Contains(got, "기록 없음") || strings.Contains(got, "Jev가 선택: +") {
		t.Fatalf("summary=%q", got)
	}
}

func TestSnapshotDecisionReadsRecordedChoice(t *testing.T) {
	chosen, status := snapshotDecision(`{"chosen_id":"b","decision_status":"received"}`)
	if chosen != "b" || status != "received" {
		t.Fatalf("%q %q", chosen, status)
	}
}
func TestDecisionTimelineGroupsEachProblemAndStep(t *testing.T) {
	frames := []Frame{{Seq: 1, Group: "0:1"}, {Seq: 2, Group: "0:1"}, {Seq: 3, Group: "0:2"}, {Seq: 4, Group: "1:1"}}
	got := decisionTimeline(frames)
	if len(got) != 3 || got[0].Seq != 2 || got[1].Seq != 3 || got[2].Seq != 4 {
		t.Fatalf("%+v", got)
	}
}

func TestMergeComparisonSnapshotKeepsTraceDecision(t *testing.T) {
	frames := []Frame{{RunID: "r", Seq: 3, Group: "2:1", Text: `{"step":1,"chosen_id":"b","decision_status":"received","candidates":[{"id":"b","lexeme":"+"}],"decision_context":{"criteria":{"b":{"lexeme":"+"}}}}`}}
	out := MergeSnapshot(frames, application.Snapshot{RunID: "r", Seq: 3, JobKind: "compare", Status: "running"})
	if !strings.Contains(out[0].Text, `"chosen_id": "b"`) || !strings.Contains(out[0].Text, `"lexeme": "+"`) || out[0].Group != "2:1" {
		t.Fatalf("lost trace: %+v", out[0])
	}
}

func TestReplayForcedDoesNotDiscardOtherParentsCandidates(t *testing.T) {
	events := []TraceEvent{{Seq: 1, RunID: "r", Kind: "search", Data: []byte(`{"id":"a","parent_id":"p","lexeme":"+","reason":"available","step":2}`)}, {Seq: 2, RunID: "r", Kind: "search", Data: []byte(`{"id":"b","parent_id":"q","lexeme":"(","reason":"forced","step":2}`)}, {Seq: 3, RunID: "r", Kind: "search", Data: []byte(`{"id":"c","parent_id":"b","lexeme":"x","reason":"available","step":2}`)}, {Seq: 4, RunID: "r", Kind: "search", Data: []byte(`{"reason":"decision_context","step":2,"decision_context":{"criteria":{"a":{},"c":{}}}}`)}, {Seq: 5, RunID: "r", Kind: "search", Data: []byte(`{"reason":"decision_result","step":2,"chosen_id":"c","decision_status":"received"}`)}}
	frames := Reconstruct(events)
	var s application.Snapshot
	if err := json.Unmarshal([]byte(frames[4].Text), &s); err != nil {
		t.Fatal(err)
	}
	if len(s.Candidates) != 2 || s.ChosenID != "c" || s.DecisionStatus != "received" || frames[4].Group == "" {
		t.Fatalf("snapshot=%+v frame=%+v", s, frames[4])
	}
}
func TestRowsKeepAll255Candidates(t *testing.T) {
	s := application.Snapshot{DecisionContext: map[string]any{"criteria": map[string]any{}}}
	criteria := s.DecisionContext["criteria"].(map[string]any)
	for i := 0; i < 255; i++ {
		id := strconv.Itoa(i)
		criteria[id] = map[string]any{"parent_id": "p"}
		s.Candidates = append(s.Candidates, application.CandidateItem{ID: id, ParentID: "p", Lexeme: "+", Reason: "available"})
	}
	rows, _ := decisionRows(s, "", "", "")
	if len(rows) != 255 {
		t.Fatalf("rows=%d", len(rows))
	}
}

func TestRecordedCodeFallsBackForLegacyAndAST(t *testing.T) {
	s := application.Snapshot{Frontier: []application.FrontierItem{{ID: "old", Code: "func old() {}"}}}
	if got := recordedCode(s, "old"); got != "func old() {}" {
		t.Fatalf("got %q", got)
	}
}

func TestComparisonProblemUsesRecordedContext(t *testing.T) {
	s := application.Snapshot{Current: &application.PublicProblem{ID: "p"}, DecisionContext: map[string]any{"intent": "배열 뒤집기", "public_tests": []any{map[string]any{"inputs": map[string]any{"xs": []any{1}}, "want": []any{1}}}}}
	p := displayProblem(s)
	if p.Intent != "배열 뒤집기" || p.PublicTests == nil {
		t.Fatalf("problem=%+v", p)
	}
}

func TestKeptCodeShowsAllChildrenAndSelectedFirst(t *testing.T) {
	s := application.Snapshot{Frontier: []application.FrontierItem{{ID: "a", ParentID: "p", Code: "A"}, {ID: "b", ParentID: "p", Code: "B"}}}
	got := keptCode(s, "p", "b")
	if !strings.Contains(got, "a") || !strings.Contains(got, "A") || !strings.Contains(got, "b") || !strings.Contains(got, "B") || strings.Index(got, "B") > strings.Index(got, "A") {
		t.Fatalf("got %q", got)
	}
}

func TestOldTraceRankMeansResponseArrivedWithoutInventingChoice(t *testing.T) {
	events := []TraceEvent{{Seq: 1, RunID: "r", Kind: "search", Data: []byte(`{"reason":"decision_context","step":2,"decision_context":{"intent":"old"}}`)}, {Seq: 2, RunID: "r", Kind: "search", Data: []byte(`{"reason":"model_started","step":2}`)}, {Seq: 3, RunID: "r", Kind: "search", Data: []byte(`{"reason":"model_finished","step":2}`)}, {Seq: 4, RunID: "r", Kind: "search", Data: []byte(`{"id":"a","lexeme":"+","reason":"ranked","rank":1,"score":0.5,"step":2}`)}}
	frames := Reconstruct(events)
	var s application.Snapshot
	if err := json.Unmarshal([]byte(frames[3].Text), &s); err != nil {
		t.Fatal(err)
	}
	if s.DecisionStatus != "received" || s.ChosenID != "" {
		t.Fatalf("snapshot=%+v", s)
	}
}

func TestDecisionHeadlineUsesActualChoiceOrForcedToken(t *testing.T) {
	s := application.Snapshot{Candidates: []application.CandidateItem{{ID: "a", Lexeme: "+", Rank: 1}, {ID: "b", Lexeme: "!=", Rank: 2}}, ForcedLexeme: "return"}
	if got := decisionHeadline(s, "b", "received"); got != "!=" {
		t.Fatalf("choice=%q", got)
	}
	if got := decisionHeadline(s, "", "forced"); got != "return" {
		t.Fatalf("forced=%q", got)
	}
	if got := decisionHeadline(s, "", "received"); got != "—" {
		t.Fatalf("old choice=%q", got)
	}
}

func TestForcedSummaryKeepsMaintainedPaths(t *testing.T) {
	s := application.Snapshot{ForcedLexeme: "return", Frontier: []application.FrontierItem{{ID: "p", Code: "return"}}}
	got := decisionSummary(s, "", "forced")
	if !strings.Contains(got, "모델 호출 없이 확정: return") || !strings.Contains(got, "p") {
		t.Fatalf("summary=%q", got)
	}
}
