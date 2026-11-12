package hypothesis

import (
	"fmt"
	"jevcancode/internal/rank"
	"math/rand"
)

// 2026-11-08: 긴 참조 경로를 균등 표본화하면서 시작과 끝을 빠뜨리지 않는다.
func sampleIndices(n, limit int) []int {
	if n <= 0 || limit <= 0 {
		return nil
	}
	if limit > n {
		limit = n
	}
	if limit == 1 {
		return []int{0}
	}
	out := make([]int, limit)
	for i := range out {
		out[i] = i * (n - 1) / (limit - 1)
	}
	return out
}

// 2026-11-08: 선택지 내용과 순서를 유지하고 ID만 바꿔 식별자 편향을 따로 관찰한다.
func remapDecision(d capturedDecision, seed int64) capturedDecision {
	out := capturedDecision{State: d.State, Candidates: append([]rank.Candidate(nil), d.Candidates...)}
	perm := rand.New(rand.NewSource(seed)).Perm(len(out.Candidates))
	ids := map[string]string{}
	for i := range out.Candidates {
		id := fmt.Sprintf("remap_%08d", perm[i])
		ids[out.Candidates[i].ID] = id
		out.Candidates[i].ID = id
	}
	for _, id := range d.ValidIDs {
		out.ValidIDs = append(out.ValidIDs, ids[id])
	}
	return out
}
