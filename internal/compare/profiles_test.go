package compare

import (
	"context"
	"tessaro/internal/rank"
	"testing"
)

func TestLocalProfileRequiresManagedResolver(t *testing.T) {
	profile := Model{ID: "local", Ranker: "local", URL: "http://127.0.0.1:8000/v1/systemone"}
	_, err := profile.NewRanker().Rank(context.Background(), "", []rank.Candidate{{ID: "a"}, {ID: "b"}})
	if err == nil || err.Error() != "local_runtime_not_configured" {
		t.Fatalf("unmanaged local model error=%v", err)
	}
}
