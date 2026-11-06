package rank

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"jevcancoding/internal/tokenlex"
	"math"
	"net/http"
	"os"
	"time"
)

type HTTP struct {
	URL, Model, APIKeyEnv string
	Timeout               time.Duration
	ReturnedModel         string
	Revision, Runtime     string
}

// 2026-11-05: 후보 전체 확률을 검증해 누락된 점수의 임의 보정을 방지한다.
func (h *HTTP) Rank(ctx context.Context, intent string, cs []Candidate) ([]Score, error) {
	return h.rank(ctx, intent, cs, nil)
}
func (h *HTTP) RankWithUsage(ctx context.Context, intent string, cs []Candidate) ([]Score, *Usage, error) {
	var usage *Usage
	s, err := h.rank(ctx, intent, cs, &usage)
	return s, usage, err
}
func (h *HTTP) rank(ctx context.Context, intent string, cs []Candidate, usage **Usage) ([]Score, error) {
	h.ReturnedModel = ""
	if len(cs) < 2 || len(cs) > 255 {
		return nil, fmt.Errorf("choice requires 2 to 255 candidates")
	}
	criteria := map[string]any{}
	seen := map[string]bool{}
	for _, c := range cs {
		if c.ID == "" || seen[c.ID] {
			return nil, fmt.Errorf("duplicate candidate")
		}
		seen[c.ID] = true
		if c.TokenKind != "" {
			if _, err := tokenlex.Kind(c.Lexeme); err != nil || c.ParentID == "" || c.Code != "" {
				return nil, fmt.Errorf("invalid token candidate")
			}
			criteria[c.ID] = map[string]string{"parent_id": c.ParentID, "token_kind": c.TokenKind, "lexeme": c.Lexeme, "rule": c.Rule}
		} else {
			criteria[c.ID] = c.Code
		}
	}
	instructions := "Choose the candidate that best satisfies the stated task."
	if cs[0].TokenKind != "" {
		instructions = "Select the next single Go lexical token that best progresses toward implementing the function described by state.intent and state.public_tests. Each option identifies its parent_id; read the current source in state.parents[parent_id].prefix and consider that source with the option's lexeme appended. Choose the option most likely to lead to a correct complete function."
	}
	payload := map[string]any{"model": h.Model, "state": intent, "questions": map[string]any{"pick": map[string]any{"type": "choice", "instructions": instructions, "criteria": criteria}}}
	b, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	timeout := h.Timeout
	if timeout <= 0 {
		timeout = 60 * time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, h.URL, bytes.NewReader(b))
	if err != nil {
		return nil, fmt.Errorf("invalid endpoint")
	}
	req.Header.Set("Content-Type", "application/json")
	if h.APIKeyEnv != "" {
		key := os.Getenv(h.APIKeyEnv)
		if key == "" {
			return nil, fmt.Errorf("API key environment variable is empty")
		}
		req.Header.Set("Authorization", "Bearer "+key)
	}
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := client.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return nil, fmt.Errorf("request_failed: %w", ctx.Err())
		}
		return nil, fmt.Errorf("request_failed")
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("rank API HTTP %d", resp.StatusCode)
	}
	var decoded struct {
		Model   string `json:"model"`
		Usage   *Usage `json:"usage"`
		Answers map[string]struct {
			Type          string              `json:"type"`
			Choice        string              `json:"choice"`
			Probabilities map[string]*float64 `json:"probabilities"`
		} `json:"answers"`
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, (1<<20)+1))
	if err != nil || len(body) > 1<<20 {
		return nil, fmt.Errorf("invalid response")
	}
	if err := rejectDuplicateKeys(body); err != nil {
		return nil, fmt.Errorf("invalid response")
	}
	dec := json.NewDecoder(bytes.NewReader(body))
	if dec.Decode(&decoded) != nil {
		return nil, fmt.Errorf("invalid response")
	}
	if decoded.Usage != nil {
		if decoded.Usage.InputTokens != nil && *decoded.Usage.InputTokens < 0 || decoded.Usage.OutputTokens != nil && *decoded.Usage.OutputTokens < 0 {
			return nil, fmt.Errorf("invalid response")
		}
		if usage != nil {
			*usage = decoded.Usage
		}
	}
	var extra any
	if dec.Decode(&extra) != io.EOF {
		return nil, fmt.Errorf("invalid response")
	}
	if decoded.Model == "" {
		return nil, fmt.Errorf("invalid response")
	}
	h.ReturnedModel = decoded.Model
	a, ok := decoded.Answers["pick"]
	if !ok || a.Type != "choice" {
		return nil, fmt.Errorf("missing choice answer")
	}
	if _, ok = criteria[a.Choice]; !ok {
		return nil, fmt.Errorf("unknown chosen candidate")
	}
	scores := make([]Score, 0, len(a.Probabilities))
	sum, maxScore := 0.0, -1.0
	for id, ptr := range a.Probabilities {
		if ptr == nil {
			return nil, fmt.Errorf("invalid probability")
		}
		v := *ptr
		if v < 0 || v > 1 {
			return nil, fmt.Errorf("invalid probability")
		}
		scores = append(scores, Score{id, v})
		sum += v
		if v > maxScore {
			maxScore = v
		}
	}
	if err = Validate(cs, scores); err != nil {
		return nil, fmt.Errorf("invalid response")
	}
	if math.Abs(sum-1) > math.Max(0.0001, float64(len(cs))*0.000051) || *a.Probabilities[a.Choice] < maxScore {
		return nil, fmt.Errorf("invalid probability distribution")
	}
	return scores, nil
}
func rejectDuplicateKeys(body []byte) error {
	d := json.NewDecoder(bytes.NewReader(body))
	var walk func() error
	walk = func() error {
		t, err := d.Token()
		if err != nil {
			return err
		}
		delim, ok := t.(json.Delim)
		if !ok {
			return nil
		}
		switch delim {
		case '{':
			seen := map[string]bool{}
			for d.More() {
				key, err := d.Token()
				if err != nil {
					return err
				}
				s, ok := key.(string)
				if !ok || seen[s] {
					return fmt.Errorf("duplicate key")
				}
				seen[s] = true
				if err = walk(); err != nil {
					return err
				}
			}
			_, err = d.Token()
			return err
		case '[':
			for d.More() {
				if err := walk(); err != nil {
					return err
				}
			}
			_, err = d.Token()
			return err
		}
		return fmt.Errorf("invalid JSON")
	}
	if err := walk(); err != nil {
		return err
	}
	if _, err := d.Token(); err != io.EOF {
		return fmt.Errorf("trailing JSON")
	}
	return nil
}

func (h *HTTP) Metadata() Metadata {
	return Metadata{Model: h.Model, ReturnedModel: h.ReturnedModel, Revision: h.Revision, Runtime: h.Runtime, ScoreSemantics: "choice probability (higher ranks first)"}
}
