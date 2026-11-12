package hypothesis

import (
	"fmt"
	"sort"
	"strings"
)

// 2026-11-08: 미완료 실행과 알려진 참조 밖 선택을 명시해 부분 결과를 확정 성능처럼 해석하지 않게 한다.
func appendEvaluationSummary(r *Report) {
	var b strings.Builder
	fmt.Fprintf(&b, "\n## 모델 선택 평가\n\n모델 요청: %d/%d\n", r.RequestsUsed, r.RequestLimit)
	if r.Ranking == nil {
		b.WriteString("선택 평가: 미실행\n")
	} else {
		unknown := 0
		for _, o := range r.Ranking.Observations {
			if o.Unknown {
				unknown++
			}
		}
		fmt.Fprintf(&b, "선택 관측: %d/%d, 미확인 선택: %d\n", len(r.Ranking.Observations), r.Ranking.Planned, unknown)
		for _, x := range []struct {
			name string
			m    *MacroMetrics
		}{{"원본 ID", r.Ranking.Original}, {"변경 ID", r.Ranking.Remapped}} {
			if x.m != nil {
				fmt.Fprintf(&b, "- %s: 참조 경로 유지율 %.3f, 우연 기대 %.3f, 차이 %.3f, bootstrap 95%% [%.3f, %.3f] (문제 %d개)\n", x.name, x.m.Top1, x.m.ChanceTop1, x.m.Delta, x.m.Lower95, x.m.Upper95, x.m.Problems)
				condition := "original"
				if x.name == "변경 ID" {
					condition = "remapped"
				}
				type aggregate struct {
					top3, mrr, chance float64
					n                 int
				}
				perProblem := map[string]aggregate{}
				for _, o := range r.Ranking.Observations {
					if o.Condition == condition {
						v := perProblem[o.ProblemID]
						v.top3 += o.Top3
						v.mrr += o.ReciprocalRank
						v.chance += o.ChanceTop3
						v.n++
						perProblem[o.ProblemID] = v
					}
				}
				var top3, mrr, chance float64
				for _, v := range perProblem {
					top3 += v.top3 / float64(v.n)
					mrr += v.mrr / float64(v.n)
					chance += v.chance / float64(v.n)
				}
				if len(perProblem) > 0 {
					n := float64(len(perProblem))
					fmt.Fprintf(&b, "  Top3 %.3f (우연 %.3f), MRR %.3f; 동점은 균등 순서 기댓값\n", top3/n, chance/n, mrr/n)
				}
			}
		}
		b.WriteString("참조에서 확인되지 않은 선택은 오답으로 확정할 수 없습니다. 선택률과 차이는 알려진 정답 기준이며 추가 정답이 있다면 달라집니다.\n")
	}
	started := 0
	status := map[string]int{}
	type counts struct{ runs, compile, public, heldout, heldoutTotal, exhaustive, sampled, sampledPass int }
	groups := map[string]counts{}
	for _, x := range r.Generation {
		status[x.Status]++
		if x.Status == "not_started" {
			continue
		}
		started++
		key := x.Ranker + " / " + x.Strategy
		v := groups[key]
		v.runs++
		if x.CompilePasses > 0 {
			v.compile++
		}
		if x.PublicPassed {
			v.public++
		}
		if x.ValidationMode == "exhaustive_public" && x.PublicPassed {
			v.exhaustive++
		}
		if x.HeldoutPassed != nil {
			v.heldoutTotal++
			if *x.HeldoutPassed {
				v.heldout++
			}
		}
		if x.ValidationMode == "sampled_holdout" {
			v.sampled++
			if x.HeldoutPassed != nil && *x.HeldoutPassed {
				v.sampledPass++
			}
		}
		groups[key] = v
	}
	fmt.Fprintf(&b, "\n## 생성 평가\n\n실행 시작: %d/%d\n", started, r.PlannedGeneration)
	keys := make([]string, 0, len(groups))
	for k := range groups {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		v := groups[k]
		fmt.Fprintf(&b, "- %s: 실행 %d, 컴파일 성공 실행 %d, 공개 통과 %d, 보류 평가 도달 실행 중 통과 %d/%d, sampled 전체 중 최종 통과 %d/%d, exhaustive public 통과 %d\n", k, v.runs, v.compile, v.public, v.heldout, v.heldoutTotal, v.sampledPass, v.sampled, v.exhaustive)
	}
	lowerGeneration := false
	if r.Status == "completed" && started == r.PlannedGeneration {
		for _, strategy := range []string{"strict-greedy", "beam"} {
			j, jok := groups["jev / "+strategy]
			random, rok := groups["random / "+strategy]
			if jok && rok && j.runs > 0 && random.runs > 0 {
				delta := float64(j.public)/float64(j.runs) - float64(random.public)/float64(random.runs)
				if delta < 0 {
					lowerGeneration = true
				}
				fmt.Fprintf(&b, "- %s 공개 통과율 차이 (Jev − 무작위): %+.3f", strategy, delta)
				if j.sampled > 0 && random.sampled > 0 {
					fmt.Fprintf(&b, ", sampled 최종 통과율 차이: %+.3f", float64(j.sampledPass)/float64(j.sampled)-float64(random.sampledPass)/float64(random.sampled))
				}
				b.WriteByte('\n')
			}
		}
	} else {
		b.WriteString("\n부분 결과이므로 무작위 기준선과의 비교는 보류합니다.\n")
	}
	keys = keys[:0]
	for k := range status {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	if len(keys) > 0 {
		b.WriteString("\n실행 상태:")
		for _, k := range keys {
			fmt.Fprintf(&b, " %s=%d", k, status[k])
		}
		b.WriteByte('\n')
	}
	b.WriteString("\n관측은 이 문제와 예산에 한정되며, 미완료 또는 미확인 결과만으로 성능 향상을 단정할 수 없습니다.\n")
	b.WriteString("선택 평가는 모델이 반환한 선택 ID를 사용하며, 생성 경로는 기존 탐색기의 점수 순서를 사용합니다.\n")
	if r.Status != "completed" {
		b.WriteString("후속 방향: 미완료 또는 차단 원인을 먼저 확인합니다.\n")
	} else if r.Ranking != nil && r.Ranking.Original != nil && r.Ranking.Original.Lower95 > 0 && lowerGeneration {
		b.WriteString("후속 방향: 선택 신호가 있어도 생성 결과가 낮은 전략은 문맥과 가지치기를 따로 실험합니다.\n")
	} else {
		b.WriteString("후속 방향: 참조 경로 신호와 생성 성능을 추가 실험으로 확인합니다.\n")
	}
	r.Summary += b.String()
}
