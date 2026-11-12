package hypothesis

import (
	"context"
	"tessaro/internal/rank"
	"tessaro/internal/tokensynth"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestInspectValidatesFortyReferences(t *testing.T) {
	r, err := Inspect(context.Background(), "../../examples/token/hypothesis.json", nil)
	if err != nil {
		t.Fatalf("inspect: %v", err)
	}
	if len(r.Problems) != 40 {
		t.Fatalf("got %d problems, want 40", len(r.Problems))
	}
	for _, p := range r.Problems {
		if p.Status != "reachable" {
			t.Errorf("reference %s is %s at %d: %s", p.ID, p.Status, p.Position, p.Reason)
		}
	}
}

func TestInspectCancelledBeforeLoading(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	report, err := Inspect(ctx, "does-not-exist.json", nil)
	if err != context.Canceled || report.Status != "cancelled" {
		t.Fatalf("got status %q and error %v", report.Status, err)
	}
}

func TestReplayCapturesProductionInputWithoutReferenceSuffix(t *testing.T) {
	s := &script{tokens: []string{"package", "main", ";", "func"}, index: 2}
	state := `{"parents":{"root":{"prefix":"package main"}}}`
	cs := []rank.Candidate{{ID: "a", Lexeme: ";"}, {ID: "b", Lexeme: "func"}}
	if _, err := s.Rank(context.Background(), state, cs); err != nil {
		t.Fatal(err)
	}
	if len(s.captures) != 1 || s.captures[0].State != state || len(s.captures[0].ValidIDs) != 1 || s.captures[0].ValidIDs[0] != "a" {
		t.Fatalf("capture: %+v", s.captures)
	}
}

func TestSampleIndicesIncludesBothEnds(t *testing.T) {
	got := sampleIndices(100, 4)
	want := []int{0, 33, 66, 99}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestReplayRecordsTargetRejectionReason(t *testing.T) {
	s := &script{tokens: []string{"x"}}
	s.trace(tokensynth.Event{Reason: "body_rejected", Lexeme: "x", RejectionReason: "invalid_body"})
	_, err := s.Rank(context.Background(), `{"parents":{"root":{"prefix":""}}}`, []rank.Candidate{{ID: "a", Lexeme: "y"}})
	if err == nil || s.reason != "body_rejected:invalid_body" {
		t.Fatalf("reason %q err %v", s.reason, err)
	}
}

func TestRemapKeepsLexemesAndValidSet(t *testing.T) {
	d := capturedDecision{State: `{"intent":"public"}`, Candidates: []rank.Candidate{{ID: "1", Lexeme: "a"}, {ID: "2", Lexeme: "b"}, {ID: "3", Lexeme: "c"}}, ValidIDs: []string{"1", "3"}}
	got := remapDecision(d, 42)
	if got.State != d.State || len(got.Candidates) != 3 || len(got.ValidIDs) != 2 {
		t.Fatalf("invalid remap: %+v", got)
	}
	for i, c := range got.Candidates {
		if c.ID == d.Candidates[i].ID || c.Lexeme != d.Candidates[i].Lexeme {
			t.Fatalf("invalid candidate: %+v", got.Candidates)
		}
	}
	valid := map[string]bool{}
	for _, id := range got.ValidIDs {
		valid[id] = true
	}
	if !valid[got.Candidates[0].ID] || valid[got.Candidates[1].ID] || !valid[got.Candidates[2].ID] {
		t.Fatalf("invalid valid set: %+v", got)
	}
}

func TestFingerprintIncludesSourceAndExecutable(t *testing.T) {
	f, e := fingerprintCode()
	if e != nil {
		t.Fatal(e)
	}
	if f["executable_sha256"] == "" || f["source:internal/tokensynth/search.go"] == "" || f["source:internal/tokensynth/bool_order.go"] == "" {
		t.Fatalf("missing fingerprints: %v", f)
	}
}

func TestFreezeRejectsTrailingManifestDocument(t *testing.T) {
	path := filepath.Join(t.TempDir(), "manifest.json")
	data := `{"schema_version":1,"model":{"id":"jev","ranker":"jev","model":"jev-1.14.0"},"references":"missing.json","problems":[{"id":"p","spec":"p.json"}],"seed":42,"budgets":{"request_limit":1,"samples_per_problem":1,"max_calls":1,"max_tokens":1,"max_candidates":1,"timeout_seconds":1}} {}`
	if e := os.WriteFile(path, []byte(data), 0600); e != nil {
		t.Fatal(e)
	}
	_, _, _, err := freeze(path)
	if err == nil || err.Error() != "invalid manifest" {
		t.Fatalf("got %v", err)
	}
}

func TestSummaryShowsPartialCountsAndUnknownInterpretation(t *testing.T) {
	r := Report{Ranking: &BResult{Planned: 4, Observations: []ObservationMetrics{{Unknown: true}}}, PlannedGeneration: 8, RequestsUsed: 1, RequestLimit: 10}
	appendEvaluationSummary(&r)
	if !strings.Contains(r.Summary, "1/4") || !strings.Contains(r.Summary, "0/8") || !strings.Contains(r.Summary, "미확인") {
		t.Fatalf("summary %q", r.Summary)
	}
}
