package hypothesis

import (
	"jevcancode/internal/rank"
	"math"
	"reflect"
	"testing"
)

func TestObservationTiesAndActualChoice(t *testing.T) {
	o, err := CalculateObservation(MetricObservation{ProblemID: "p", CandidateIDs: []string{"a", "b", "c", "d"}, ValidIDs: []string{"b", "c"}, Scores: []rank.Score{{ID: "a", Score: 1}, {ID: "b", Score: .5}, {ID: "c", Score: .5}, {ID: "d", Score: .5}}, ChosenID: "a"})
	if err != nil {
		t.Fatal(err)
	}
	if o.Top1 != 0 || math.Abs(o.Top3-1) > 1e-9 || math.Abs(o.ReciprocalRank-(2.0/3*0.5+1.0/3/3)) > 1e-9 || o.BestRankMin != 2 || o.BestRankMax != 3 || o.ChanceTop1 != .5 || o.ChanceTop3 != 1 {
		t.Fatalf("metrics %+v", o)
	}
}
func TestObservationUnknownAndInvalid(t *testing.T) {
	base := MetricObservation{ProblemID: "p", CandidateIDs: []string{"a", "b", "c", "d"}, ValidIDs: []string{"a"}, Scores: []rank.Score{{ID: "a", Score: 1}, {ID: "b", Score: 1}, {ID: "c", Score: 1}, {ID: "d", Score: 1}}, ChosenID: "b"}
	o, e := CalculateObservation(base)
	if e != nil || !o.Unknown || math.Abs(o.Top3-.75) > 1e-9 {
		t.Fatalf("unknown/tie %+v %v", o, e)
	}
	base.Scores[0].Score = math.NaN()
	if _, e = CalculateObservation(base); e == nil {
		t.Fatal("NaN accepted")
	}
	if _, e = CalculateObservation(MetricObservation{}); e == nil {
		t.Fatal("empty accepted")
	}
}
func TestBootstrapMacroDeterministicAndEmpty(t *testing.T) {
	obs := []ObservationMetrics{{ProblemID: "a", Top1: 1, ChanceTop1: .5}, {ProblemID: "a", Top1: 0, ChanceTop1: .5}, {ProblemID: "b", Top1: 1, ChanceTop1: .25}}
	a, e := BootstrapMacro(obs, 42)
	if e != nil {
		t.Fatal(e)
	}
	b, e := BootstrapMacro(obs, 42)
	if e != nil || !reflect.DeepEqual(a, b) || a.Problems != 2 || a.Top1 != .75 || a.Delta != .375 {
		t.Fatalf("a=%+v b=%+v err=%v", a, b, e)
	}
	if _, e = BootstrapMacro(nil, 42); e == nil {
		t.Fatal("empty accepted")
	}
}

func TestObservationRejectsInconsistentIDsAndInfiniteScores(t *testing.T) {
	base := MetricObservation{ProblemID: "p", CandidateIDs: []string{"a", "b"}, ValidIDs: []string{"a"}, Scores: []rank.Score{{ID: "a", Score: 1}, {ID: "b", Score: 0}}, ChosenID: "b"}
	base.ChosenID = "missing"
	if _, err := CalculateObservation(base); err == nil {
		t.Fatal("unknown chosen id accepted")
	}
	base.ChosenID = "b"
	base.Scores[1].Score = math.Inf(1)
	if _, err := CalculateObservation(base); err == nil {
		t.Fatal("infinite score accepted")
	}
	base.Scores[1] = rank.Score{ID: "a", Score: 0}
	if _, err := CalculateObservation(base); err == nil {
		t.Fatal("duplicate score id accepted")
	}
	base.ChosenID = ""
	if _, err := CalculateObservation(base); err == nil {
		t.Fatal("missing choice accepted")
	}
}

func TestUnknownChoiceIncludedInBootstrap(t *testing.T) {
	o, err := CalculateObservation(MetricObservation{ProblemID: "p", CandidateIDs: []string{"a", "b"}, ValidIDs: []string{"a"}, Scores: []rank.Score{{ID: "a", Score: .7}, {ID: "b", Score: .3}}, ChosenID: "b"})
	if err != nil || !o.Unknown || o.Top1 != 0 {
		t.Fatalf("observation=%+v error=%v", o, err)
	}
	m, err := BootstrapMacro([]ObservationMetrics{o}, 1)
	if err != nil || m.Delta != -.5 {
		t.Fatalf("macro=%+v error=%v", m, err)
	}
}

func TestObservationRejectsOutOfRangeScores(t *testing.T) {
	o := MetricObservation{ProblemID: "p", CandidateIDs: []string{"a"}, ValidIDs: []string{"a"}, Scores: []rank.Score{{ID: "a", Score: 1.1}}, ChosenID: "a"}
	if _, err := CalculateObservation(o); err == nil {
		t.Fatal("out of range score accepted")
	}
}
