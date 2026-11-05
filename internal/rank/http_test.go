package rank

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestHTTPRejectsMissingProbability(t *testing.T) {
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"model":"test","answers":{"pick":{"type":"choice","choice":"a","probabilities":{"a":1}}}}`))
	}))
	defer s.Close()
	_, err := (&HTTP{URL: s.URL, Model: "test"}).Rank(context.Background(), "x", []Candidate{{ID: "a", Code: "a"}, {ID: "b", Code: "b"}})
	if err == nil {
		t.Fatal("missing probability accepted")
	}
}
func TestHTTPRejectsDuplicateProbabilityID(t *testing.T) {
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"model":"test","answers":{"pick":{"type":"choice","choice":"a","probabilities":{"a":0.5,"a":0.5,"b":0.5}}}}`))
	}))
	defer s.Close()
	_, err := (&HTTP{URL: s.URL, Model: "test"}).Rank(context.Background(), "x", []Candidate{{ID: "a", Code: "a"}, {ID: "b", Code: "b"}})
	if err == nil {
		t.Fatal("duplicate probability ID accepted")
	}
}
func TestHTTPDistributionValidation(t *testing.T) {
	cases := []struct {
		name, body string
		valid      bool
	}{{"rounded", `{"model":"test","answers":{"pick":{"type":"choice","choice":"a","probabilities":{"a":0.3334,"b":0.3333,"c":0.3333}}}}`, true}, {"bad sum", `{"model":"test","answers":{"pick":{"type":"choice","choice":"a","probabilities":{"a":0.5,"b":0.2,"c":0.2}}}}`, false}, {"negative", `{"model":"test","answers":{"pick":{"type":"choice","choice":"a","probabilities":{"a":1,"b":-0.1,"c":0.1}}}}`, false}, {"nonfinite", `{"model":"test","answers":{"pick":{"type":"choice","choice":"a","probabilities":{"a":NaN,"b":0,"c":0}}}}`, false}, {"not max", `{"model":"test","answers":{"pick":{"type":"choice","choice":"b","probabilities":{"a":0.8,"b":0.1,"c":0.1}}}}`, false}}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte(tc.body)) }))
			defer s.Close()
			_, err := (&HTTP{URL: s.URL, Model: "test"}).Rank(context.Background(), "x", []Candidate{{ID: "a", Code: "a"}, {ID: "b", Code: "b"}, {ID: "c", Code: "c"}})
			if (err == nil) != tc.valid {
				t.Fatalf("error %v", err)
			}
		})
	}
}
func TestHTTPCancelAndStatus(t *testing.T) {
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusBadRequest) }))
	defer s.Close()
	cs := []Candidate{{ID: "a", Code: "a"}, {ID: "b", Code: "b"}}
	_, err := (&HTTP{URL: s.URL}).Rank(context.Background(), "x", cs)
	if err == nil {
		t.Fatal("status accepted")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = (&HTTP{URL: s.URL}).Rank(ctx, "x", cs)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel lost: %v", err)
	}
}
func TestHTTPRejectsNullProbabilityAndModel(t *testing.T) {
	for _, body := range []string{`{"model":"test","answers":{"pick":{"type":"choice","choice":"a","probabilities":{"a":1,"b":null}}}}`, `{"model":null,"answers":{"pick":{"type":"choice","choice":"a","probabilities":{"a":0.5,"b":0.5}}}}`} {
		s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte(body)) }))
		_, err := (&HTTP{URL: s.URL, Model: "test"}).Rank(context.Background(), "x", []Candidate{{ID: "a", Code: "a"}, {ID: "b", Code: "b"}})
		s.Close()
		if err == nil {
			t.Fatal("null accepted")
		}
	}
}
func TestHTTPReportsOptionalUsage(t *testing.T) {
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"model":"test","usage":{"input_tokens":12,"output_tokens":3},"answers":{"pick":{"type":"choice","choice":"a","probabilities":{"a":0.5,"b":0.5}}}}`))
	}))
	defer s.Close()
	_, u, err := (&HTTP{URL: s.URL, Model: "test"}).RankWithUsage(context.Background(), "x", []Candidate{{ID: "a", Code: "a"}, {ID: "b", Code: "b"}})
	if err != nil || u == nil || u.InputTokens == nil || *u.InputTokens != 12 || u.OutputTokens == nil || *u.OutputTokens != 3 {
		t.Fatalf("%+v %v", u, err)
	}
}
