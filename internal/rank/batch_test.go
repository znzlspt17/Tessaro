package rank

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestHTTPBatchTwoIndependentQuestionsOneRequest(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		var body struct {
			State     any `json:"state"`
			Questions map[string]struct {
				Instructions map[string]any `json:"instructions"`
				Criteria     map[string]any `json:"criteria"`
			} `json:"questions"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
			return
		}
		if len(body.Questions) != 2 || body.Questions["q0"].Instructions["parent_state"] != "first" || body.Questions["q1"].Instructions["parent_state"] != "second" {
			t.Errorf("questions=%+v", body.Questions)
		}
		_, _ = w.Write([]byte(`{"model":"test","usage":{"input_tokens":12,"output_tokens":4},"answers":{"q0":{"type":"choice","choice":"b","probabilities":{"a":0.4,"b":0.6}},"q1":{"type":"choice","choice":"c","probabilities":{"c":0.8,"d":0.2}}}}`))
	}))
	defer server.Close()
	var base Ranker = &HTTP{URL: server.URL, Model: "test"}
	batch, ok := base.(BatchDecisionRanker)
	if !ok {
		t.Fatal("HTTP does not expose batch")
	}
	ds, u, err := batch.RankDecisions(context.Background(), []DecisionRequest{{State: "first", Candidates: []Candidate{{ID: "a"}, {ID: "b"}}}, {State: "second", Candidates: []Candidate{{ID: "c"}, {ID: "d"}}}})
	if err != nil || requests != 1 || len(ds) != 2 || ds[0].ChosenID != "b" || ds[1].ChosenID != "c" || ds[0].Usage != nil || ds[1].Usage != nil || u == nil || *u.InputTokens != 12 || *u.OutputTokens != 4 {
		t.Fatalf("requests=%d ds=%+v usage=%+v err=%v", requests, ds, u, err)
	}
}

func TestHTTPBatchRejectsInvalidAnswerSets(t *testing.T) {
	for _, body := range []string{
		`{"model":"test","answers":{"q0":{"type":"choice","choice":"a","probabilities":{"a":1,"b":0}}}}`,
		`{"model":"test","answers":{"q0":{"type":"choice","choice":"a","probabilities":{"a":1,"b":0}},"q1":{"type":"choice","choice":"c","probabilities":{"c":1,"d":0}},"extra":{}}}`,
		`{"model":"test","answers":{"q0":{"type":"choice","choice":"a","probabilities":{"a":1,"b":0}},"q0":{"type":"choice","choice":"a","probabilities":{"a":1,"b":0}},"q1":{"type":"choice","choice":"c","probabilities":{"c":1,"d":0}}}}`,
		`{"model":"test","answers":{"q0":{"type":"choice","choice":"a","probabilities":{"a":1,"b":0}},"q1":{"type":"choice","choice":"d","probabilities":{"c":1,"d":0}}}}`,
	} {
		t.Run(body[:30], func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(body)) }))
			defer server.Close()
			var base Ranker = &HTTP{URL: server.URL, Model: "test"}
			batch, ok := base.(BatchDecisionRanker)
			if !ok {
				t.Fatal("HTTP does not expose batch")
			}
			_, _, err := batch.RankDecisions(context.Background(), []DecisionRequest{{Candidates: []Candidate{{ID: "a"}, {ID: "b"}}}, {Candidates: []Candidate{{ID: "c"}, {ID: "d"}}}})
			var safe interface{ ErrorCode() string }
			if !errors.As(err, &safe) || safe.ErrorCode() != "invalid_response" || strings.Contains(err.Error(), body) {
				t.Fatalf("unsafe or missing error: %v", err)
			}
		})
	}
}

func TestHTTPBatchCancellation(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { <-r.Context().Done() }))
	defer server.Close()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	h := &HTTP{URL: server.URL, Model: "test"}
	_, _, err := h.RankDecisions(ctx, []DecisionRequest{{Candidates: []Candidate{{ID: "a"}, {ID: "b"}}}, {Candidates: []Candidate{{ID: "c"}, {ID: "d"}}}})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation lost: %v", err)
	}
}

func TestHTTPBatchRejectsOverLimitQuestionWithoutRequest(t *testing.T) {
	called := 0
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { called++ }))
	defer server.Close()
	cs := make([]Candidate, 256)
	for i := range cs {
		cs[i] = Candidate{ID: fmt.Sprintf("c%d", i)}
	}
	h := &HTTP{URL: server.URL, Model: "test"}
	_, _, err := h.RankDecisions(context.Background(), []DecisionRequest{{Candidates: cs}, {Candidates: []Candidate{{ID: "a"}, {ID: "b"}}}})
	if err == nil || called != 0 || h.HTTPRequests() != 0 {
		t.Fatalf("limit hidden: calls=%d requests=%d err=%v", called, h.HTTPRequests(), err)
	}
}
