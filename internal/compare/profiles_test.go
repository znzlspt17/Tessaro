package compare

import (
	"jevcancode/internal/rank"
	"testing"
)

func TestLocalProfileDefaultsToDecider(t *testing.T) {
	profile := Model{ID: "local", Ranker: "local", URL: "http://127.0.0.1:8000/v1/systemone"}
	r := profile.NewRanker().(*rank.HTTP)
	if r.Model != "Mapika/decider-4b" {
		t.Fatalf("local model default=%q", r.Model)
	}
}
