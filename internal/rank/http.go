package rank

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"jevcancode/internal/tokenlex"
	"math"
	"net/http"
	"os"
	"sync/atomic"
	"time"
)

type HTTP struct {
	URL, Model, APIKeyEnv string
	Timeout               time.Duration
	ReturnedModel         string
	Revision, Runtime     string
	requests              int64
}

func (h *HTTP) HTTPRequests() int64 { return atomic.LoadInt64(&h.requests) }

// 2026-11-12: Choice 응답은 검증된 전체 범주 분포만 노출한다.
func (*HTTP) NormalizedCategorical() bool { return true }

// 2026-11-05: 후보 전체 확률을 검증해 누락된 점수의 임의 보정을 방지한다.
func (h *HTTP) Rank(ctx context.Context, intent string, cs []Candidate) ([]Score, error) {
	d, err := h.RankDecision(ctx, intent, cs)
	return d.Scores, err
}
func (h *HTTP) RankWithUsage(ctx context.Context, intent string, cs []Candidate) ([]Score, *Usage, error) {
	d, err := h.RankDecision(ctx, intent, cs)
	return d.Scores, d.Usage, err
}
func (h *HTTP) RankDecision(ctx context.Context, intent string, cs []Candidate) (Decision, error) {
	var d Decision
	s, err := h.rank(ctx, intent, cs, &d)
	d.Scores = s
	return d, err
}
func (h *HTTP) rank(ctx context.Context, intent string, cs []Candidate, decision *Decision) ([]Score, error) {
	h.ReturnedModel = ""
	b, criteria, err := choiceRequest(h.Model, intent, cs)
	if err != nil {
		return nil, err
	}
	body, err := h.post(ctx, b)
	if err != nil {
		return nil, err
	}
	var chosen string
	scores, returned, u, err := choiceResponse(body, criteria, cs, &chosen)
	if err != nil {
		return nil, &SafeError{Code: "invalid_response", Message: "invalid response"}
	}
	h.ReturnedModel = returned
	decision.Usage, decision.ChosenID = u, chosen
	return scores, nil
}

// 2026-11-12: 단일·batch 전송에 동일한 시간 제한과 비밀 없는 오류 처리를 적용한다.
func (h *HTTP) post(ctx context.Context, b []byte) ([]byte, error) {
	timeout := h.Timeout
	if timeout <= 0 {
		timeout = 60 * time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, h.URL, bytes.NewReader(b))
	if err != nil {
		return nil, &SafeError{Code: "invalid_endpoint", Message: "invalid endpoint"}
	}
	req.Header.Set("Content-Type", "application/json")
	if h.APIKeyEnv != "" {
		key := os.Getenv(h.APIKeyEnv)
		if key == "" {
			return nil, &SafeError{Code: "auth_unavailable", Message: "API key environment variable is empty"}
		}
		req.Header.Set("Authorization", "Bearer "+key)
	}
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	atomic.AddInt64(&h.requests, 1)
	resp, err := client.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			code := "request_timeout"
			if ctx.Err() == context.Canceled {
				code = "request_cancelled"
			}
			return nil, &SafeError{Code: code, Message: "request_failed", Cause: ctx.Err()}
		}
		return nil, &SafeError{Code: "request_failed", Message: "request_failed"}
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, &SafeError{Code: fmt.Sprintf("http_%d", resp.StatusCode), Message: fmt.Sprintf("rank API HTTP %d", resp.StatusCode)}
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, (1<<20)+1))
	if err != nil || len(body) > 1<<20 {
		return nil, &SafeError{Code: "invalid_response", Message: "invalid response"}
	}
	return body, nil
}

// 2026-11-07: HTTP와 로컬 워커에 같은 후보 계약을 적용한다.
func ChoiceQuestion(cs []Candidate) (string, map[string]any, error) {
	if len(cs) < 2 || len(cs) > 255 {
		return "", nil, fmt.Errorf("choice requires 2 to 255 candidates")
	}
	criteria := map[string]any{}
	seen := map[string]bool{}
	for _, c := range cs {
		if c.ID == "" || seen[c.ID] {
			return "", nil, fmt.Errorf("duplicate candidate")
		}
		seen[c.ID] = true
		if c.TokenKind != "" {
			if _, err := tokenlex.Kind(c.Lexeme); err != nil || c.ParentID == "" || c.Code != "" {
				return "", nil, fmt.Errorf("invalid token candidate")
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
	return instructions, criteria, nil
}

// 2026-11-07: 화면과 HTTP·로컬 워커가 동일한 선택 지시와 후보를 사용한다.
func choiceRequest(model, intent string, cs []Candidate) ([]byte, map[string]any, error) {
	instructions, criteria, err := ChoiceQuestion(cs)
	if err != nil {
		return nil, nil, err
	}
	payload := map[string]any{"model": model, "state": intent, "questions": map[string]any{"pick": map[string]any{"type": "choice", "instructions": instructions, "criteria": criteria}}}
	b, err := json.Marshal(payload)
	return b, criteria, err
}

// 2026-11-07: 전송 방식과 무관하게 응답 전체를 동일하게 검증한다.
func choiceResponse(body []byte, criteria map[string]any, cs []Candidate, chosen *string) ([]Score, string, *Usage, error) {
	var decoded struct {
		Model   string `json:"model"`
		Usage   *Usage `json:"usage"`
		Answers map[string]struct {
			Type          string              `json:"type"`
			Choice        string              `json:"choice"`
			Probabilities map[string]*float64 `json:"probabilities"`
		} `json:"answers"`
	}
	if err := rejectDuplicateKeys(body); err != nil {
		return nil, "", nil, fmt.Errorf("invalid response")
	}
	dec := json.NewDecoder(bytes.NewReader(body))
	if dec.Decode(&decoded) != nil {
		return nil, "", nil, fmt.Errorf("invalid response")
	}
	if decoded.Usage != nil {
		if decoded.Usage.InputTokens != nil && *decoded.Usage.InputTokens < 0 || decoded.Usage.OutputTokens != nil && *decoded.Usage.OutputTokens < 0 {
			return nil, "", nil, fmt.Errorf("invalid response")
		}
	}
	var extra any
	if dec.Decode(&extra) != io.EOF {
		return nil, "", nil, fmt.Errorf("invalid response")
	}
	if decoded.Model == "" {
		return nil, "", nil, fmt.Errorf("invalid response")
	}
	a, ok := decoded.Answers["pick"]
	if !ok || a.Type != "choice" {
		return nil, "", nil, fmt.Errorf("missing choice answer")
	}
	if _, ok = criteria[a.Choice]; !ok {
		return nil, "", nil, fmt.Errorf("unknown chosen candidate")
	}
	scores := make([]Score, 0, len(a.Probabilities))
	sum, maxScore := 0.0, -1.0
	for id, ptr := range a.Probabilities {
		if ptr == nil {
			return nil, "", nil, fmt.Errorf("invalid probability")
		}
		v := *ptr
		if v < 0 || v > 1 {
			return nil, "", nil, fmt.Errorf("invalid probability")
		}
		scores = append(scores, Score{id, v})
		sum += v
		if v > maxScore {
			maxScore = v
		}
	}
	if err := Validate(cs, scores); err != nil {
		return nil, "", nil, fmt.Errorf("invalid response")
	}
	if math.Abs(sum-1) > math.Max(0.0001, float64(len(cs))*0.000051) || *a.Probabilities[a.Choice] < maxScore {
		return nil, "", nil, fmt.Errorf("invalid probability distribution")
	}
	if chosen != nil {
		*chosen = a.Choice
	}
	return scores, decoded.Model, decoded.Usage, nil
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
