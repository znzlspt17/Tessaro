package hypothesis

import (
	"errors"
	"tessaro/internal/rank"
	"math"
	"math/rand"
	"sort"
)

type MetricObservation struct {
	ProblemID              string
	CandidateIDs, ValidIDs []string
	Scores                 []rank.Score
	ChosenID               string
}
type ObservationMetrics struct {
	ProblemID      string  `json:"problem_id"`
	Condition      string  `json:"condition,omitempty"`
	Step           int     `json:"step"`
	Top1           float64 `json:"top1"`
	Top3           float64 `json:"top3"`
	ReciprocalRank float64 `json:"reciprocal_rank"`
	ChanceTop1     float64 `json:"chance_top1"`
	ChanceTop3     float64 `json:"chance_top3"`
	Unknown        bool    `json:"unknown"`
	BestRankMin    int     `json:"best_rank_min"`
	BestRankMax    int     `json:"best_rank_max"`
}
type MacroMetrics struct {
	Top1       float64 `json:"top1"`
	ChanceTop1 float64 `json:"chance_top1"`
	Delta      float64 `json:"delta"`
	Lower95    float64 `json:"lower_95"`
	Upper95    float64 `json:"upper_95"`
	Problems   int     `json:"problems"`
}

// 2026-11-08: 동점 안의 순서를 임의로 확정하지 않아 정답 순위와 Top3가 낙관적으로 왜곡되지 않게 한다.
func CalculateObservation(o MetricObservation) (ObservationMetrics, error) {
	fail := func() (ObservationMetrics, error) { return ObservationMetrics{}, errors.New("invalid_observation") }
	n := len(o.CandidateIDs)
	if o.ProblemID == "" || n == 0 || len(o.Scores) != n || len(o.ValidIDs) == 0 {
		return fail()
	}
	ids := map[string]bool{}
	for _, id := range o.CandidateIDs {
		if id == "" || ids[id] {
			return fail()
		}
		ids[id] = true
	}
	valid := map[string]bool{}
	for _, id := range o.ValidIDs {
		if !ids[id] || valid[id] {
			return fail()
		}
		valid[id] = true
	}
	if o.ChosenID == "" || !ids[o.ChosenID] {
		return fail()
	}
	scores := append([]rank.Score(nil), o.Scores...)
	seen := map[string]bool{}
	for _, s := range scores {
		if !ids[s.ID] || seen[s.ID] || math.IsNaN(s.Score) || math.IsInf(s.Score, 0) || s.Score < 0 || s.Score > 1 {
			return fail()
		}
		seen[s.ID] = true
	}
	sort.Slice(scores, func(i, j int) bool { return scores[i].Score > scores[j].Score })
	m := ObservationMetrics{ProblemID: o.ProblemID, Unknown: !valid[o.ChosenID], ChanceTop1: float64(len(valid)) / float64(n)}
	if valid[o.ChosenID] {
		m.Top1 = 1
	}
	draws := min(3, n)
	miss := 1.0
	for i := 0; i < draws; i++ {
		miss *= float64(n-len(valid)-i) / float64(n-i)
		if n-len(valid)-i <= 0 {
			miss = 0
			break
		}
	}
	m.ChanceTop3 = 1 - miss
	for i := 0; i < n; {
		j := i + 1
		for j < n && scores[j].Score == scores[i].Score {
			j++
		}
		k := 0
		for _, s := range scores[i:j] {
			if valid[s.ID] {
				k++
			}
		}
		if k > 0 {
			g := j - i
			m.BestRankMin = i + 1
			m.BestRankMax = i + g - k + 1
			remainingMiss := 1.0
			for pos := 1; pos <= g-k+1; pos++ {
				p := remainingMiss * float64(k) / float64(g-pos+1)
				m.ReciprocalRank += p / float64(i+pos)
				if i+pos <= 3 {
					m.Top3 += p
				}
				remainingMiss *= float64(g-pos+1-k) / float64(g-pos+1)
			}
			break
		}
		i = j
	}
	return m, nil
}

// 2026-11-08: 동일 문제의 여러 관측을 먼저 평균하여 관측 수가 많은 문제가 전체 추정치를 지배하지 않게 한다.
func BootstrapMacro(observations []ObservationMetrics, seed int64) (MacroMetrics, error) {
	type group struct {
		top, chance float64
		count       int
	}
	groups := map[string]group{}
	for _, o := range observations {
		if o.ProblemID == "" || math.IsNaN(o.Top1) || math.IsInf(o.Top1, 0) || math.IsNaN(o.ChanceTop1) || math.IsInf(o.ChanceTop1, 0) || o.Top1 < 0 || o.Top1 > 1 || o.ChanceTop1 < 0 || o.ChanceTop1 > 1 {
			return MacroMetrics{}, errors.New("invalid_observation")
		}
		g := groups[o.ProblemID]
		g.top += o.Top1
		g.chance += o.ChanceTop1
		g.count++
		groups[o.ProblemID] = g
	}
	if len(groups) == 0 {
		return MacroMetrics{}, errors.New("no_observations")
	}
	keys := make([]string, 0, len(groups))
	for k := range groups {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	deltas := make([]float64, len(keys))
	result := MacroMetrics{Problems: len(keys)}
	for i, k := range keys {
		g := groups[k]
		top := g.top / float64(g.count)
		chance := g.chance / float64(g.count)
		result.Top1 += top
		result.ChanceTop1 += chance
		deltas[i] = top - chance
	}
	result.Top1 /= float64(len(keys))
	result.ChanceTop1 /= float64(len(keys))
	result.Delta = result.Top1 - result.ChanceTop1
	rng := rand.New(rand.NewSource(seed))
	samples := make([]float64, 10000)
	for i := range samples {
		for range deltas {
			samples[i] += deltas[rng.Intn(len(deltas))]
		}
		samples[i] /= float64(len(deltas))
	}
	sort.Float64s(samples)
	result.Lower95 = samples[249]
	result.Upper95 = samples[9749]
	return result, nil
}
