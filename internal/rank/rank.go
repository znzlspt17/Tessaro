package rank

import (
	"context"
	"fmt"
	"math"
	"sort"
)

type Candidate struct {
	ID        string `json:"id"`
	Code      string `json:"code,omitempty"`
	Size      int    `json:"-"`
	ParentID  string `json:"parent_id,omitempty"`
	TokenKind string `json:"token_kind,omitempty"`
	Lexeme    string `json:"lexeme,omitempty"`
	Rule      string `json:"rule,omitempty"`
}
type Score struct {
	ID    string  `json:"id"`
	Score float64 `json:"score"`
}
type Ranker interface {
	Rank(context.Context, string, []Candidate) ([]Score, error)
}

// CategoricalRanker는 점수가 정규화된 범주 분포라는 명시적 계약이다.
type CategoricalRanker interface{ NormalizedCategorical() bool }

// 2026-11-12: 사용자에게 응답 본문이나 네트워크 내부 오류를 노출하지 않는 식별자다.
type SafeError struct {
	Code, Message string
	Cause         error
}

func (e *SafeError) Error() string     { return e.Message }
func (e *SafeError) Unwrap() error     { return e.Cause }
func (e *SafeError) ErrorCode() string { return e.Code }

// HTTPRequestCounter는 실제 HTTP 전송 횟수를 관측할 수 있는 공급자에만 구현한다.
type HTTPRequestCounter interface{ HTTPRequests() int64 }

func Validate(cs []Candidate, ss []Score) error {
	if len(cs) != len(ss) {
		return fmt.Errorf("score count mismatch")
	}
	ids := map[string]bool{}
	for _, c := range cs {
		if c.ID == "" || ids[c.ID] {
			return fmt.Errorf("invalid candidate id")
		}
		ids[c.ID] = true
	}
	seen := map[string]bool{}
	for _, s := range ss {
		if !ids[s.ID] || seen[s.ID] || math.IsNaN(s.Score) || math.IsInf(s.Score, 0) {
			return fmt.Errorf("invalid score for %q", s.ID)
		}
		seen[s.ID] = true
	}
	return nil
}
func Order(cs []Candidate, ss []Score) ([]Candidate, error) {
	if err := Validate(cs, ss); err != nil {
		return nil, err
	}
	scores := map[string]float64{}
	for _, s := range ss {
		scores[s.ID] = s.Score
	}
	out := append([]Candidate(nil), cs...)
	sort.SliceStable(out, func(i, j int) bool {
		a, b := scores[out[i].ID], scores[out[j].ID]
		if a == b {
			if out[i].Size != out[j].Size {
				return out[i].Size < out[j].Size
			}
			return out[i].ID < out[j].ID
		}
		return a > b
	})
	return out, nil
}

type Fake struct{}

func (Fake) Rank(_ context.Context, _ string, cs []Candidate) ([]Score, error) {
	s := make([]Score, len(cs))
	for i, c := range cs {
		s[i] = Score{c.ID, float64(len(cs) - i)}
	}
	return s, nil
}

type Metadata struct {
	Model          string `json:"model"`
	ReturnedModel  string `json:"returned_model,omitempty"`
	Revision       string `json:"revision,omitempty"`
	Runtime        string `json:"runtime,omitempty"`
	ScoreSemantics string `json:"score_semantics"`
}
type MetadataProvider interface{ Metadata() Metadata }

func (Fake) Metadata() Metadata {
	return Metadata{Model: "fake", ScoreSemantics: "deterministic test order (not model performance)"}
}

type Usage struct {
	InputTokens  *int `json:"input_tokens"`
	OutputTokens *int `json:"output_tokens"`
}
type UsageRanker interface {
	RankWithUsage(context.Context, string, []Candidate) ([]Score, *Usage, error)
}

// Decision은 각 호출에서 검증된 모델의 실제 선택과 점수를 함께 전달한다.
type Decision struct {
	Scores   []Score
	ChosenID string
	Usage    *Usage
}

type DecisionRanker interface {
	RankDecision(context.Context, string, []Candidate) (Decision, error)
}

type DecisionRequest struct {
	State      string
	Candidates []Candidate
}

// BatchDecisionRanker는 상호 독립적인 질문의 결정을 한 호출로 반환한다.
type BatchDecisionRanker interface {
	RankDecisions(context.Context, []DecisionRequest) ([]Decision, *Usage, error)
}

// BatchCapability lets wrappers preserve whether transport batching is available.
type BatchCapability interface{ SupportsDecisionBatch() bool }
