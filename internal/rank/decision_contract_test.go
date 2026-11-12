package rank

import (
	"context"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
)

func TestChoiceTransportExposesPerRequestDecision(t *testing.T) {
	for _, transport := range []any{&HTTP{}, &Worker{}} {
		if _, ok := reflect.TypeOf(transport).MethodByName("RankDecision"); !ok {
			t.Errorf("%T does not expose the returned choice for this request", transport)
		}
	}
}

func TestHTTPDecisionPreservesChosenIDForTiedScores(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"model":"test","answers":{"pick":{"type":"choice","choice":"b","probabilities":{"a":0.5,"b":0.5}}}}`))
	}))
	defer server.Close()
	cs := []Candidate{{ID: "a", Code: "a"}, {ID: "b", Code: "b"}}
	d, err := (&HTTP{URL: server.URL, Model: "test"}).RankDecision(context.Background(), "x", cs)
	if err != nil || d.ChosenID != "b" {
		t.Fatalf("decision: %+v, err %v", d, err)
	}
	ordered, err := Order(cs, d.Scores)
	if err != nil || ordered[0].ID != "a" {
		t.Fatalf("tie ordering changed: %+v %v", ordered, err)
	}
}
