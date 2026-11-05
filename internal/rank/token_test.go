package rank

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestTokenRequestDoesNotSendExpandedCodeOptions(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Questions map[string]struct {
				Criteria map[string]json.RawMessage `json:"criteria"`
			} `json:"questions"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		for _, raw := range body.Questions["pick"].Criteria {
			var option map[string]any
			if err := json.Unmarshal(raw, &option); err != nil {
				t.Errorf("option must be token object: %s", raw)
			}
			if option["lexeme"] == nil || option["parent_id"] == nil || option["code"] != nil {
				t.Errorf("not fragment-only option: %s", raw)
			}
		}
		w.Write([]byte(`{"model":"test","answers":{"pick":{"type":"choice","choice":"a","probabilities":{"a":0.8,"b":0.2}}}}`))
	}))
	defer server.Close()
	_, err := (&HTTP{URL: server.URL, Model: "test"}).Rank(context.Background(), "parent context", []Candidate{{ID: "a", ParentID: "p0", TokenKind: "-", Lexeme: "-", Rule: "binary_expression"}, {ID: "b", ParentID: "p0", TokenKind: "+", Lexeme: "+", Rule: "binary_expression"}})
	if err != nil {
		t.Fatal(err)
	}
}
