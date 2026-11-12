package hypothesis

import (
	"context"
	"jevcancode/internal/rank"
)

type BResult struct {
	Observations []ObservationMetrics `json:"observations"`
	Macro        *MacroMetrics        `json:"macro,omitempty"`
	Original     *MacroMetrics        `json:"original,omitempty"`
	Remapped     *MacroMetrics        `json:"remapped,omitempty"`
	Planned      int                  `json:"planned"`
}

// 2026-11-08: 같은 생산 state를 그대로 재사용하고 ID만 재배정해 모델 선택의 민감도를 측정한다.
func runB(ctx context.Context, captures map[string][]capturedDecision, ids []string, m manifest, model *guardedRanker, progress func(Progress)) (out BResult, err error) {
	defer func() {
		if v, e := BootstrapMacro(out.Observations, m.Seed); e == nil {
			out.Macro = &v
		}
		for _, condition := range []string{"original", "remapped"} {
			var selected []ObservationMetrics
			for _, observation := range out.Observations {
				if observation.Condition == condition {
					selected = append(selected, observation)
				}
			}
			if v, e := BootstrapMacro(selected, m.Seed); e == nil {
				if condition == "original" {
					out.Original = &v
				} else {
					out.Remapped = &v
				}
			}
		}
	}()
	total := 0
	for _, id := range ids {
		total += 2 * len(sampleIndices(len(captures[id]), m.Budgets.Samples))
	}
	out.Planned = total
	completed := 0
	for problemIndex, id := range ids {
		for _, index := range sampleIndices(len(captures[id]), m.Budgets.Samples) {
			base := captures[id][index]
			for condition := 0; condition < 2; condition++ {
				if e := ctx.Err(); e != nil {
					return out, e
				}
				d := base
				kind := "original"
				if condition == 1 {
					kind = "remapped"
					d = remapDecision(base, m.Seed+int64(problemIndex)*100000+int64(index))
				}
				if progress != nil {
					progress(Progress{Phase: "ranking", ProblemID: id, Completed: completed, Total: total, RequestsUsed: model.used, RequestLimit: model.limit, Event: &DetailEvent{Phase: "ranking", ProblemID: id, Kind: kind + "_request", Step: index, State: d.State, Candidates: d.Candidates}})
				}
				decision, e := model.RankDecision(ctx, d.State, d.Candidates)
				if e != nil {
					return out, e
				}
				ids := make([]string, len(d.Candidates))
				for i, c := range d.Candidates {
					ids[i] = c.ID
				}
				obs, e := CalculateObservation(MetricObservation{ProblemID: id, CandidateIDs: ids, ValidIDs: d.ValidIDs, Scores: decision.Scores, ChosenID: decision.ChosenID})
				if e != nil {
					model.failure = errService
					return out, model.failure
				}
				obs.Condition = kind
				obs.Step = index
				out.Observations = append(out.Observations, obs)
				completed++
				if progress != nil {
					progress(Progress{Phase: "ranking", ProblemID: id, Completed: completed, Total: total, RequestsUsed: model.used, RequestLimit: model.limit, Event: &DetailEvent{Phase: "ranking", ProblemID: id, Kind: kind + "_decision", Step: index, Scores: append([]rank.Score(nil), decision.Scores...), ChosenID: decision.ChosenID, Usage: decision.Usage}})
				}
			}
		}
	}
	return out, nil
}
