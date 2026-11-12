package benchmark

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"jevcancode/internal/jsoninput"
	"jevcancode/internal/rank"
	"sort"
	"time"
)

type Problem struct {
	ID         string           `json:"id"`
	Intent     string           `json:"intent"`
	CodePrefix string           `json:"code_prefix,omitempty"`
	Candidates []rank.Candidate `json:"candidates"`
	Answers    []string         `json:"answers"`
}
type Manifest struct {
	Problems []Problem `json:"problems"`
}
type Item struct {
	LatencyMS   float64        `json:"latency_ms"`
	AnswerRanks map[string]int `json:"answer_ranks,omitempty"`
	Scores      []rank.Score   `json:"scores,omitempty"`
	Metadata    rank.Metadata  `json:"metadata"`
	ID          string         `json:"id"`
	Rank        int            `json:"rank,omitempty"`
	Error       string         `json:"error,omitempty"`
}
type Report struct {
	EvaluationKind     string  `json:"evaluation_kind"`
	Top3Recall         float64 `json:"top3_recall"`
	Top3HitRate        float64 `json:"top3_hit_rate"`
	RequestFailureRate float64 `json:"request_failure_rate"`
	Total              int     `json:"total"`
	Failed             int     `json:"failed"`
	Top1               float64 `json:"top1"`
	MRR                float64 `json:"mrr"`
	Items              []Item  `json:"items"`
}

func Decode(r io.Reader) (Manifest, error) {
	var m Manifest
	d := json.NewDecoder(jsoninput.Reader(r))
	d.DisallowUnknownFields()
	if err := d.Decode(&m); err != nil {
		return m, err
	}
	var x any
	if d.Decode(&x) != io.EOF {
		return m, fmt.Errorf("trailing JSON")
	}
	return m, m.Validate()
}
func (m Manifest) Validate() error {
	if len(m.Problems) == 0 {
		return fmt.Errorf("empty benchmark")
	}
	ids := map[string]bool{}
	for _, p := range m.Problems {
		if p.ID == "" || p.Intent == "" || ids[p.ID] || len(p.Candidates) < 4 || len(p.Candidates) > 255 {
			return fmt.Errorf("invalid problem %q", p.ID)
		}
		ids[p.ID] = true
		cids := map[string]bool{}
		for _, c := range p.Candidates {
			if c.ID == "" || c.Code == "" || cids[c.ID] {
				return fmt.Errorf("invalid candidate")
			}
			cids[c.ID] = true
		}
		if len(p.Answers) == 0 {
			return fmt.Errorf("answers required")
		}
		seen := map[string]bool{}
		for _, a := range p.Answers {
			if !cids[a] || seen[a] {
				return fmt.Errorf("invalid answer")
			}
			seen[a] = true
		}
	}
	return nil
}

// 2026-11-05: 실패도 전체 문제 수에 포함하여 모델 오류를 성공률에서 숨기지 않는다.
func Run(ctx context.Context, m Manifest, r rank.Ranker) Report {
	return RunWithProgress(ctx, m, r, nil)
}

// 2026-11-07: 실행 진행 알림은 기록과 분리해 완료된 문제 수만 전달한다.
func RunWithProgress(ctx context.Context, m Manifest, r rank.Ranker, progress func(int, int)) Report {
	out := Report{Total: len(m.Problems), EvaluationKind: "complete_candidate_selection"}
	var hits, rr, recall, top3hits float64
	for _, p := range m.Problems {
		item := Item{ID: p.ID}
		// 2026-11-07: 취소 후 추가 추론을 막고 원시 모델 진단이 결과에 저장되지 않게 한다.
		if ctx.Err() != nil {
			item.Error = "not_run_cancelled"
			out.Items = append(out.Items, item)
			if progress != nil {
				progress(len(out.Items), out.Total)
			}
			continue
		}
		start := time.Now()
		state := p.Intent
		if p.CodePrefix != "" {
			b, _ := json.Marshal(struct {
				Intent     string `json:"intent"`
				CodePrefix string `json:"code_prefix"`
			}{p.Intent, p.CodePrefix})
			state = string(b)
		}
		scores, err := r.Rank(ctx, state, p.Candidates)
		item.LatencyMS = float64(time.Since(start)) / float64(time.Millisecond)
		if provider, ok := r.(rank.MetadataProvider); ok {
			item.Metadata = provider.Metadata()
		}
		var ordered []rank.Candidate
		if err == nil {
			ordered, err = rank.Order(p.Candidates, scores)
		}
		if err != nil {
			out.Failed++
			item.Error = "model_error"
			out.Items = append(out.Items, item)
			if progress != nil {
				progress(len(out.Items), out.Total)
			}
			continue
		}
		answers := map[string]bool{}
		for _, a := range p.Answers {
			answers[a] = true
		}
		sort.Slice(scores, func(i, j int) bool { return scores[i].ID < scores[j].ID })
		item.Scores = scores
		item.AnswerRanks = map[string]int{}
		top3 := 0
		for i, c := range ordered {
			if answers[c.ID] {
				item.AnswerRanks[c.ID] = i + 1
				if i < 3 {
					top3++
				}
				if item.Rank == 0 {
					item.Rank = i + 1
					rr += 1 / float64(i+1)
					if i == 0 {
						hits++
					}
				}
			}
		}
		recall += float64(top3) / float64(len(p.Answers))
		if top3 > 0 {
			top3hits++
		}
		out.Items = append(out.Items, item)
		if progress != nil {
			progress(len(out.Items), out.Total)
		}
	}
	if out.Total > 0 {
		out.Top1 = hits / float64(out.Total)
		out.MRR = rr / float64(out.Total)
		out.Top3Recall = recall / float64(out.Total)
		out.Top3HitRate = top3hits / float64(out.Total)
		out.RequestFailureRate = float64(out.Failed) / float64(out.Total)
	}
	return out
}
