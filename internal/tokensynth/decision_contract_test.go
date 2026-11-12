package tokensynth

import (
	"context"
	"errors"
	"testing"

	"jevcancode/internal/rank"
)

type decisionFixture struct {
	fail    bool
	invalid bool
}

func (d decisionFixture) Rank(ctx context.Context, s string, cs []rank.Candidate) ([]rank.Score, error) {
	v, err := d.RankDecision(ctx, s, cs)
	return v.Scores, err
}
func (d decisionFixture) RankDecision(_ context.Context, _ string, cs []rank.Candidate) (rank.Decision, error) {
	if d.fail {
		return rank.Decision{}, errors.New("fixture failure")
	}
	scores := make([]rank.Score, len(cs))
	for i, c := range cs {
		scores[i] = rank.Score{ID: c.ID, Score: 1}
	}
	if d.invalid {
		scores = scores[:len(scores)-1]
	}
	return rank.Decision{Scores: scores, ChosenID: cs[len(cs)-1].ID}, nil
}

func TestDecisionResultRecordsExplicitChoiceAfterScoreValidation(t *testing.T) {
	for _, tc := range []struct {
		name    string
		fixture decisionFixture
		status  string
	}{
		{"tie", decisionFixture{}, "received"}, {"failed", decisionFixture{fail: true}, "failed"}, {"invalid", decisionFixture{invalid: true}, "failed"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var events []Event
			_, _ = Search(context.Background(), subtractionSpec(t), tc.fixture, Options{Strategy: "strict-greedy", MaxCalls: 1, Trace: func(e Event) { events = append(events, e) }}, func(string) (bool, error) { return false, nil })
			var result *Event
			var candidates []string
			for i := range events {
				if events[i].Reason == "available" {
					candidates = append(candidates, events[i].ID)
				}
				if events[i].Reason == "decision_result" {
					result = &events[i]
					break
				}
			}
			if result == nil || result.DecisionStatus != tc.status {
				t.Fatalf("missing %s result: %+v", tc.status, result)
			}
			if tc.status == "received" && (len(candidates) < 2 || result.ChosenID != candidates[len(candidates)-1]) {
				t.Fatalf("wrong explicit choice: %+v %v", result, candidates)
			}
			if tc.status == "failed" && result.ChosenID != "" {
				t.Fatalf("failed call retained choice: %+v", result)
			}
		})
	}
}
