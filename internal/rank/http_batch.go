package rank

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

func (*HTTP) SupportsDecisionBatch() bool { return true }

// BatchQuestion exposes the exact per-question structure used by HTTP batching.
func BatchQuestion(request DecisionRequest) (map[string]any, error) {
	instructions, criteria, err := ChoiceQuestion(request.Candidates)
	if err != nil {
		return nil, err
	}
	var state any = request.State
	var structured any
	if json.Unmarshal([]byte(request.State), &structured) == nil {
		state = structured
	}
	return map[string]any{"type": "choice", "instructions": map[string]any{"parent_state": state, "question": "Use only parent_state for this independent question; do not use other questions or their answers. " + strings.ReplaceAll(instructions, "state.", "parent_state.")}, "criteria": criteria}, nil
}

// 2026-11-12: 부모별 문맥을 각 질문 지시에 담아 서로의 답을 전제로 하지 않는 선택만 묶는다.
func (h *HTTP) RankDecisions(ctx context.Context, requests []DecisionRequest) ([]Decision, *Usage, error) {
	if len(requests) == 0 {
		return nil, nil, fmt.Errorf("empty batch")
	}
	questions := make(map[string]any, len(requests))
	criteria := make([]map[string]any, len(requests))
	for i, request := range requests {
		question, e := BatchQuestion(request)
		if e != nil {
			return nil, nil, e
		}
		criteria[i] = question["criteria"].(map[string]any)
		questions[fmt.Sprintf("q%d", i)] = question
	}
	payload, err := json.Marshal(map[string]any{"model": h.Model, "state": "Each question contains its own independent parent_state.", "questions": questions})
	if err != nil {
		return nil, nil, err
	}
	h.ReturnedModel = ""
	body, err := h.post(ctx, payload)
	if err != nil {
		return nil, nil, err
	}
	invalid := func() ([]Decision, *Usage, error) {
		return nil, nil, &SafeError{Code: "invalid_response", Message: "invalid response"}
	}
	if rejectDuplicateKeys(body) != nil {
		return invalid()
	}
	var response struct {
		Model   string                     `json:"model"`
		Usage   *Usage                     `json:"usage"`
		Answers map[string]json.RawMessage `json:"answers"`
	}
	if json.Unmarshal(body, &response) != nil || response.Model == "" || len(response.Answers) != len(requests) {
		return invalid()
	}
	decisions := make([]Decision, len(requests))
	for i, request := range requests {
		answer, ok := response.Answers[fmt.Sprintf("q%d", i)]
		if !ok {
			return invalid()
		}
		individual, e := json.Marshal(map[string]any{"model": response.Model, "usage": response.Usage, "answers": map[string]json.RawMessage{"pick": answer}})
		if e != nil {
			return invalid()
		}
		var chosen string
		scores, _, _, e := choiceResponse(individual, criteria[i], request.Candidates, &chosen)
		if e != nil {
			return invalid()
		}
		decisions[i] = Decision{Scores: scores, ChosenID: chosen}
	}
	h.ReturnedModel = response.Model
	return decisions, response.Usage, nil
}
