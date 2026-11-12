package rank

import (
	"math"
	"testing"
)

func TestValidateMissingCandidate(t *testing.T) {
	if Validate([]Candidate{{ID: "a"}, {ID: "b"}}, []Score{{ID: "a", Score: 1}}) == nil {
		t.Fatal("missing candidate accepted")
	}
}
func TestOrderRejectsDuplicateAndUnknownIDs(t *testing.T) {
	cs := []Candidate{{ID: "a"}, {ID: "b"}}
	for _, scores := range [][]Score{{{ID: "a", Score: 1}, {ID: "a", Score: 0}}, {{ID: "a", Score: 1}, {ID: "c", Score: 0}}, {{ID: "a", Score: math.NaN()}, {ID: "b", Score: 0}}} {
		if _, err := Order(cs, scores); err == nil {
			t.Fatal("invalid score accepted")
		}
	}
}
func TestOrderUsesStableIDTieBreak(t *testing.T) {
	out, err := Order([]Candidate{{ID: "b"}, {ID: "a"}}, []Score{{ID: "a", Score: 1}, {ID: "b", Score: 1}})
	if err != nil || out[0].ID != "a" {
		t.Fatalf("%v %v", out, err)
	}
}

func TestNormalizedCategoricalCapability(t *testing.T) {
	for _, r := range []Ranker{&HTTP{}, NewWorker(WorkerConfig{})} {
		capability, ok := r.(interface{ NormalizedCategorical() bool })
		if !ok || !capability.NormalizedCategorical() {
			t.Fatalf("%T does not declare categorical distribution", r)
		}
	}
	if capability, ok := any(Fake{}).(interface{ NormalizedCategorical() bool }); ok && capability.NormalizedCategorical() {
		t.Fatal("fake arbitrary ranks declared probabilities")
	}
}
