package tokensynth

import (
	"container/heap"
	"context"
	"encoding/json"
	"fmt"
	"math"
	"math/rand"
	"sort"
	"strconv"

	"tessaro/internal/rank"
	"tessaro/internal/tokenspec"
	"tessaro/internal/tokenverify"
)

type searchNode struct {
	state    *State
	rankCost int
	logProb  float64
	sequence int
	priority float64
	parent   *searchNode
	children []*searchNode
	prior    float64
	visits   int
	value    float64
	dead     bool
}

// 2026-11-12: 완료된 가지는 재선택하지 않고 공개 평가의 보상을 조상까지 누적한다.
func retire(n *searchNode, reward float64) {
	for x := n; x != nil; x = x.parent {
		x.visits++
		x.value += reward
	}
	n.dead = true
	for x := n.parent; x != nil; x = x.parent {
		all := len(x.children) > 0
		for _, c := range x.children {
			if !c.dead {
				all = false
				break
			}
		}
		if !all {
			break
		}
		x.dead = true
	}
}

func samplePolicy(children []*searchNode, rng *rand.Rand) *searchNode {
	total := 0.0
	for _, c := range children {
		total += c.prior
	}
	draw := rng.Float64() * total
	for _, c := range children {
		draw -= c.prior
		if draw <= 0 {
			return c
		}
	}
	return children[len(children)-1]
}
func selectMCTS(root *searchNode, rng *rand.Rand) *searchNode {
	n := root
	for len(n.children) > 0 {
		var unseen []*searchNode
		for _, c := range n.children {
			if !c.dead && c.visits == 0 {
				unseen = append(unseen, c)
			}
		}
		if len(unseen) > 0 {
			n = samplePolicy(unseen, rng)
			continue
		}
		var best *searchNode
		bestScore := math.Inf(-1)
		for _, c := range n.children {
			if c.dead {
				continue
			}
			score := c.value/float64(c.visits) + 1.414*c.prior*math.Sqrt(float64(n.visits))/(1+float64(c.visits))
			if score > bestScore {
				best, bestScore = c, score
			}
		}
		if best == nil {
			return nil
		}
		n = best
	}
	return n
}

type searchQueue []*searchNode

type preparedExpansion struct {
	choices    []Choice
	candidates []rank.Candidate
}

// 2026-11-12: 선행 요청에도 동일한 문제, 부모 문맥, 순서 및 모델 정체성을 cache key로 사용한다.
func researchQuestion(p tokenspec.Problem, r rank.Ranker, s *State, candidates []rank.Candidate) (rank.DecisionRequest, string, error) {
	contextData := map[string]any{"intent": p.Intent, "inputs": p.Inputs, "allowed_types": p.AllowedTypes, "literals": p.Literals, "type_hints": p.TypeHints, "output_type_hint": p.OutputTypeHint, "public_tests": p.PublicTests, "parents": map[string]any{s.ID: parentDecisionContext(p, s)}}
	addReturnPolicyContext(contextData, p)
	data, _ := json.Marshal(contextData)
	instructions, criteria, err := rank.ChoiceQuestion(candidates)
	if err != nil {
		return rank.DecisionRequest{}, "", err
	}
	var metadata any
	if provider, ok := r.(rank.MetadataProvider); ok {
		identity := provider.Metadata()
		identity.ReturnedModel = ""
		metadata = identity
	}
	key, _ := json.Marshal([]any{p, string(data), candidates, instructions, criteria, metadata})
	return rank.DecisionRequest{State: string(data), Candidates: candidates}, string(key), nil
}

func researchTraceContext(request rank.DecisionRequest, batched bool) map[string]any {
	context := map[string]any{}
	if json.Unmarshal([]byte(request.State), &context) != nil {
		context["state"] = request.State
	}
	context["state_json"] = request.State
	context["schema_version"] = "decision-context-v1"
	context["ordered_candidates"] = request.Candidates
	context["transport"] = "single"
	if batched {
		context["transport"] = "batch"
		context["batch_state"] = "Each question contains its own independent parent_state."
		question, _ := rank.BatchQuestion(request)
		context["instructions"] = question["instructions"]
		context["criteria"] = question["criteria"]
	} else {
		instructions, criteria, _ := rank.ChoiceQuestion(request.Candidates)
		context["instructions"] = instructions
		context["criteria"] = criteria
	}
	return context
}

func validateResearchDecision(strategy string, cs []rank.Candidate, d rank.Decision) error {
	if err := rank.Validate(cs, d.Scores); err != nil {
		return err
	}
	scores := map[string]float64{}
	sum, max := 0.0, math.Inf(-1)
	for _, s := range d.Scores {
		scores[s.ID] = s.Score
		if s.Score > max {
			max = s.Score
		}
		if strategy != "lds" {
			if s.Score < 0 || s.Score > 1 {
				return fmt.Errorf("invalid categorical probability")
			}
			sum += s.Score
		}
	}
	if strategy != "lds" && math.Abs(sum-1) > math.Max(.0001, float64(len(cs))*.000051) {
		return fmt.Errorf("invalid categorical probability sum")
	}
	if d.ChosenID != "" {
		v, ok := scores[d.ChosenID]
		if !ok {
			return fmt.Errorf("unknown chosen candidate")
		}
		if v < max {
			return fmt.Errorf("chosen candidate below maximum")
		}
	}
	return nil
}

func (q searchQueue) Len() int { return len(q) }
func (q searchQueue) Less(i, j int) bool {
	if q[i].priority != q[j].priority {
		return q[i].priority < q[j].priority
	}
	if len(q[i].state.Tokens) != len(q[j].state.Tokens) {
		return len(q[i].state.Tokens) > len(q[j].state.Tokens)
	}
	return q[i].sequence < q[j].sequence
}
func (q searchQueue) Swap(i, j int) { q[i], q[j] = q[j], q[i] }
func (q *searchQueue) Push(x any)   { *q = append(*q, x.(*searchNode)) }
func (q *searchQueue) Pop() any     { old := *q; n := len(old); x := old[n-1]; *q = old[:n-1]; return x }

// 2026-11-12: 신규 탐색기는 부모별 정책을 유지하여 기존 beam의 결합 선택과 확률을 혼동하지 않는다.
func researchSearch(ctx context.Context, p tokenspec.Problem, r rank.Ranker, o Options, check func(string) (bool, error), initial []*State) (out Result, err error) {
	step := 0
	if trace := o.Trace; trace != nil {
		o.Trace = func(event Event) { event.Step = step; trace(event) }
	}
	if err = p.Validate(); err != nil {
		return
	}
	if err = ctx.Err(); err != nil {
		return
	}
	if o.MaxTokens <= 0 {
		o.MaxTokens = 256
	}
	if o.MaxCalls <= 0 {
		o.MaxCalls = 128
	}
	if o.MaxCandidates <= 0 {
		o.MaxCandidates = 8192
	}
	if o.Strategy != "lds" {
		c, ok := r.(rank.CategoricalRanker)
		if !ok || !c.NormalizedCategorical() {
			return out, fmt.Errorf("normalized categorical probabilities required")
		}
	}
	if o.Strategy == "mcts" && o.PublicEvaluate == nil {
		return out, fmt.Errorf("public evaluator required")
	}
	if o.DecisionBatchSize < 0 || o.DecisionBatchSize > 256 {
		return out, fmt.Errorf("invalid decision batch size")
	}
	if o.Strategy == "mcts" && o.DecisionBatchSize > 1 {
		return out, fmt.Errorf("mcts decision batch unsupported")
	}
	if initial == nil {
		var root *State
		root, err = NewState()
		if err != nil {
			return
		}
		initial = []*State{root}
	}
	q := searchQueue{}
	heap.Init(&q)
	seq := 0
	for _, s := range initial {
		if s != nil {
			heap.Push(&q, &searchNode{state: s, sequence: seq})
			seq++
		}
	}
	var root *searchNode
	if o.Strategy == "mcts" {
		if q.Len() != 1 {
			return out, fmt.Errorf("mcts requires one root")
		}
		root = heap.Pop(&q).(*searchNode)
	}
	rng := rand.New(rand.NewSource(o.Seed))
	var rollout *searchNode
	evaluations := map[string]tokenverify.PublicEvaluation{}
	decisions := map[string]rank.Decision{}
	prefetched := map[string]bool{}
	prepared := map[*searchNode]preparedExpansion{}
	usedIDs := map[string]bool{}
	for _, s := range initial {
		if s != nil {
			usedIDs[s.ID] = true
			usedIDs[s.ParentID] = true
		}
	}
	idsByExpansion := map[string][]string{}
	var nextID uint64
	// 2026-11-12: 선행 확장도 즉시 예산에 반영하고 실제 pop 시 재계산하지 않는다.
	expand := func(s *State) (preparedExpansion, error) {
		raw, rejected, exhausted, e := candidatesWithin(p, s, o.MaxCandidates-out.Candidates)
		out.Expansions++
		if e != nil {
			return preparedExpansion{}, e
		}
		out.Candidates += len(rejected) + len(raw)
		if exhausted {
			return preparedExpansion{}, ErrBudget
		}
		result := preparedExpansion{}
		for _, c := range raw {
			tokens := append(append([]string(nil), s.Tokens...), c.Lexeme)
			if tokenverify.CheckReturnPrefix(p, tokens) != "" || s.irrecoverableExpression(c.Lexeme) || (orderingOperator(c.Lexeme) && s.comparisonResultBefore(len(s.Tokens))) {
				continue
			}
			if (c.Lexeme == ";" || c.Lexeme == "}" || c.Lexeme == "{" || c.Lexeme == "(") && tokenverify.CheckBodyPrefix(p, tokens) != "" {
				continue
			}
			result.choices = append(result.choices, c)
		}
		if len(result.choices) > 255 {
			return preparedExpansion{}, ErrCandidateLimit
		}
		for _, c := range result.choices {
			result.candidates = append(result.candidates, rank.Candidate{ParentID: s.ID, TokenKind: c.Kind, Lexeme: c.Lexeme, Rule: c.Rule, Size: len(s.Tokens) + 1})
		}
		// 2026-11-12: 부모 경로를 반복하는 식별자 비용을 없애고 동일 부모 재확장은 동일 ID로 memo를 보존한다.
		expansionKey, _ := json.Marshal([]any{s.ID, s.Tokens, result.candidates})
		ids, exists := idsByExpansion[string(expansionKey)]
		if !exists {
			ids = make([]string, len(result.candidates))
			for i := range ids {
				for {
					nextID++
					id := fmt.Sprintf("n%016x", nextID)
					if !usedIDs[id] {
						usedIDs[id] = true
						ids[i] = id
						break
					}
				}
			}
			idsByExpansion[string(expansionKey)] = ids
		}
		for i := range result.candidates {
			result.candidates[i].ID = ids[i]
		}
		return result, nil
	}
	limited := false
	frontier := func(current *searchNode) {
		if o.Trace == nil {
			return
		}
		nodes := append(searchQueue(nil), q...)
		paths := []Path{}
		if current != nil {
			paths = append(paths, Path{ID: current.state.ID, ParentID: current.state.ParentID, Rank: 1, TokenCount: len(current.state.Tokens)})
		}
		if root != nil {
			stack := []*searchNode{root}
			for len(stack) > 0 {
				node := stack[len(stack)-1]
				stack = stack[:len(stack)-1]
				if node.dead {
					continue
				}
				if len(node.children) == 0 {
					if node != current {
						paths = append(paths, Path{ID: node.state.ID, ParentID: node.state.ParentID, Rank: len(paths) + 1, TokenCount: len(node.state.Tokens)})
					}
					continue
				}
				for i := len(node.children) - 1; i >= 0; i-- {
					stack = append(stack, node.children[i])
				}
			}
		}
		for nodes.Len() > 0 {
			node := heap.Pop(&nodes).(*searchNode)
			paths = append(paths, Path{ID: node.state.ID, ParentID: node.state.ParentID, Rank: len(paths) + 1, TokenCount: len(node.state.Tokens)})
		}
		o.Trace(Event{ID: "frontier", Reason: "frontier", Frontier: paths, Calls: out.Calls, Candidates: out.Candidates})
	}
	frontier(root)
	for q.Len() > 0 || (root != nil && !root.dead) {
		if err = ctx.Err(); err != nil {
			return
		}
		var n *searchNode
		if root != nil {
			if rollout != nil {
				n = rollout
				rollout = nil
			} else {
				n = selectMCTS(root, rng)
			}
			if n == nil {
				break
			}
		} else {
			n = heap.Pop(&q).(*searchNode)
		}
		step++
		s := n.state
		if o.Trace != nil {
			event := Event{ID: s.ID, ParentID: s.ParentID, Reason: "visit", Calls: out.Calls, Candidates: out.Candidates}
			if o.TraceCode {
				event.Code = s.Source()
			}
			o.Trace(event)
		}
		frontier(n)
		if tokenverify.CheckReturnPrefix(p, s.Tokens) != "" || tokenverify.CheckBodyPrefix(p, s.Tokens) != "" || s.priorBoolOrdering() != nil || s.irrecoverableExpression("") {
			if root != nil {
				retire(n, 0)
			}
			continue
		}
		var declaration tokenverify.DeclarationCheck
		declaration, err = s.checkDeclaration(p)
		if err != nil {
			return
		}
		if declaration.Status == tokenverify.DeclarationRejected {
			if root != nil {
				retire(n, 0)
			}
			continue
		}
		if Complete(s) {
			if root != nil {
				eval, exists := evaluations[s.Source()]
				if !exists {
					out.Validations++
					if o.Trace != nil {
						o.Trace(Event{ID: s.ID, ParentID: s.ParentID, Reason: "public_evaluation_started", Calls: out.Calls, Candidates: out.Candidates})
					}
					eval, err = o.PublicEvaluate(ctx, s.Source())
					if err != nil {
						return
					}
					evaluations[s.Source()] = eval
				} else {
					out.EvaluationMemoHits++
				}
				reward := 0.0
				if eval.Status == "completed" && eval.Total > 0 && eval.Passed >= 0 && eval.Passed <= eval.Total {
					reward = float64(eval.Passed) / float64(eval.Total)
				}
				if o.Trace != nil {
					o.Trace(Event{ID: s.ID, ParentID: s.ParentID, Reason: "public_evaluated", Status: eval.Status, Score: &reward, Calls: out.Calls, Candidates: out.Candidates})
				}
				retire(n, reward)
				if reward < 1 {
					if o.Trace != nil {
						o.Trace(Event{ID: s.ID, ParentID: s.ParentID, Reason: "public_failed", Calls: out.Calls, Candidates: out.Candidates})
					}
					continue
				}
			}
			var ok bool
			out.Validations++
			if o.Trace != nil {
				o.Trace(Event{ID: s.ID, ParentID: s.ParentID, Reason: "validation_started", Calls: out.Calls, Candidates: out.Candidates})
			}
			ok, err = check(s.Source())
			if o.Trace != nil {
				o.Trace(Event{ID: s.ID, ParentID: s.ParentID, Reason: "validation_finished", Calls: out.Calls, Candidates: out.Candidates})
			}
			if err != nil {
				return
			}
			if ok {
				out.Source = s.Source()
				out.Tokens = append([]string(nil), s.Tokens...)
				out.ForcedTokens = s.Forced
				out.ModelSelectedTokens = s.Selected
				if o.Trace != nil {
					o.Trace(Event{ID: s.ID, ParentID: s.ParentID, Reason: "public_passed", Calls: out.Calls, Candidates: out.Candidates})
				}
				frontier(n)
				return out, nil
			}
			if o.Trace != nil {
				o.Trace(Event{ID: s.ID, ParentID: s.ParentID, Reason: "public_failed", Calls: out.Calls, Candidates: out.Candidates})
			}
			continue
		}
		if len(s.Tokens) >= o.MaxTokens {
			limited = true
			if root != nil {
				retire(n, 0)
			}
			continue
		}
		expansion, cached := prepared[n]
		if cached {
			delete(prepared, n)
		} else {
			expansion, err = expand(s)
			if err != nil {
				return out, err
			}
		}
		choices, candidates := expansion.choices, expansion.candidates
		if len(choices) == 0 {
			if root != nil {
				retire(n, 0)
			}
			continue
		}
		if o.Trace != nil && len(choices) > 1 {
			for _, c := range candidates {
				o.Trace(Event{ID: c.ID, ParentID: s.ID, Reason: "available", Lexeme: c.Lexeme, TokenKind: c.TokenKind, Rule: c.Rule, Calls: out.Calls, Candidates: out.Candidates})
			}
		}
		ordered := candidates
		scores := map[string]float64{}
		chosen := ""
		if len(choices) > 1 {
			request, key, e := researchQuestion(p, r, s, candidates)
			if e != nil {
				return out, e
			}
			d, hit := decisions[key]
			if hit {
				out.MemoHits++
				if o.Trace != nil {
					phase := "memo"
					if prefetched[key] {
						phase = "prefetch"
					}
					o.Trace(Event{ID: "model", ParentID: s.ID, Reason: "decision_context", Phase: phase, DecisionContext: researchTraceContext(request, prefetched[key]), Calls: out.Calls, Candidates: out.Candidates})
				}
			} else {
				if out.Calls >= o.MaxCalls {
					return out, ErrBudget
				}
				batch, canBatch := r.(rank.BatchDecisionRanker)
				if capability, ok := r.(rank.BatchCapability); ok && !capability.SupportsDecisionBatch() {
					canBatch = false
				}
				requests := []rank.DecisionRequest{request}
				keys := []string{key}
				known := map[string]bool{key: true}
				if root == nil && canBatch && o.DecisionBatchSize > 1 {
					// 2026-11-12: 현재 frontier의 독립 부모만 미리 질문하며 실행하지 않은 확장도 예산에 포함한다.
					peek := append(searchQueue(nil), q...)
					for peek.Len() > 0 && len(requests) < o.DecisionBatchSize && len(requests) < o.MaxCalls-out.Calls {
						if e = ctx.Err(); e != nil {
							return out, e
						}
						other := heap.Pop(&peek).(*searchNode)
						parent := other.state
						if Complete(parent) || len(parent.Tokens) >= o.MaxTokens || tokenverify.CheckReturnPrefix(p, parent.Tokens) != "" || tokenverify.CheckBodyPrefix(p, parent.Tokens) != "" || parent.priorBoolOrdering() != nil || parent.irrecoverableExpression("") {
							continue
						}
						decl, de := parent.checkDeclaration(p)
						if de != nil {
							return out, de
						}
						if decl.Status == tokenverify.DeclarationRejected {
							continue
						}
						pre, exists := prepared[other]
						if !exists {
							if out.Candidates >= o.MaxCandidates {
								break
							}
							pre, e = expand(parent)
							if e != nil {
								if e == ErrBudget {
									break
								}
								return out, e
							}
							prepared[other] = pre
						}
						if len(pre.choices) < 2 {
							continue
						}
						next, nextKey, qe := researchQuestion(p, r, parent, pre.candidates)
						if qe != nil {
							return out, qe
						}
						if _, exists = decisions[nextKey]; exists || known[nextKey] {
							continue
						}
						known[nextKey] = true
						requests = append(requests, next)
						keys = append(keys, nextKey)
					}
				}
				if o.Trace != nil {
					currentContext := researchTraceContext(request, len(requests) > 1)
					if len(requests) > 1 {
						currentContext["question_id"] = "q0"
					}
					o.Trace(Event{ID: "model", ParentID: s.ID, Reason: "decision_context", DecisionContext: currentContext, Calls: out.Calls, Candidates: out.Candidates})
					for i := 1; i < len(requests); i++ {
						prefetchContext := researchTraceContext(requests[i], true)
						prefetchContext["question_id"] = fmt.Sprintf("q%d", i)
						o.Trace(Event{ID: "model", ParentID: requests[i].Candidates[0].ParentID, Reason: "prefetch_context", Phase: "prefetch", DecisionContext: prefetchContext, Calls: out.Calls, Candidates: out.Candidates})
					}
					o.Trace(Event{ID: "model", ParentID: s.ID, Reason: "model_started", Calls: out.Calls, Candidates: out.Candidates})
				}
				if len(requests) > 1 {
					out.Calls += len(requests)
					var ds []rank.Decision
					ds, _, err = batch.RankDecisions(ctx, requests)
					if err == nil && len(ds) != len(requests) {
						err = fmt.Errorf("batch decision count mismatch")
					}
					if err == nil {
						for i := range ds {
							if err = validateResearchDecision(o.Strategy, requests[i].Candidates, ds[i]); err != nil {
								break
							}
						}
					}
					if err == nil {
						for i := range ds {
							decisions[keys[i]] = ds[i]
							if i > 0 {
								prefetched[keys[i]] = true
								if o.Trace != nil {
									o.Trace(Event{ID: "model", ParentID: requests[i].Candidates[0].ParentID, Reason: "prefetch_result", Phase: "prefetch", DecisionStatus: "received", ChosenID: ds[i].ChosenID, Calls: out.Calls, Candidates: out.Candidates})
								}
							}
						}
						d = ds[0]
					}
				} else {
					out.Calls++
					if dr, ok := r.(rank.DecisionRanker); ok {
						d, err = dr.RankDecision(ctx, request.State, candidates)
					} else {
						d.Scores, err = r.Rank(ctx, request.State, candidates)
					}
				}
				if err != nil {
					if questionBudgetRejected(err) {
						out.RejectedQuestions += len(requests)
					}
					if o.Trace != nil {
						for i := 1; i < len(requests); i++ {
							o.Trace(Event{ID: "model", ParentID: requests[i].Candidates[0].ParentID, Reason: "prefetch_result", Phase: "prefetch", DecisionStatus: "failed", Calls: out.Calls, Candidates: out.Candidates})
						}
						o.Trace(Event{ID: "model", ParentID: s.ID, Reason: "decision_result", DecisionStatus: "failed", Calls: out.Calls, Candidates: out.Candidates})
					}
					return
				}
			}
			if err = validateResearchDecision(o.Strategy, candidates, d); err != nil {
				if o.Trace != nil {
					o.Trace(Event{ID: "model", ParentID: s.ID, Reason: "decision_result", DecisionStatus: "failed", Calls: out.Calls, Candidates: out.Candidates})
				}
				return
			}
			if o.Trace != nil {
				phase := ""
				if hit {
					phase = "memo"
					if prefetched[key] {
						phase = "prefetch"
					}
				}
				o.Trace(Event{ID: "model", ParentID: s.ID, Reason: "decision_result", Phase: phase, DecisionStatus: "received", ChosenID: d.ChosenID, Calls: out.Calls, Candidates: out.Candidates})
			}
			ss := d.Scores
			chosen = d.ChosenID
			ordered, err = rank.Order(candidates, ss)
			if err != nil {
				return
			}
			sum := 0.0
			for _, v := range ss {
				scores[v.ID] = v.Score
				if o.Strategy != "lds" {
					if v.Score < 0 || v.Score > 1 {
						return out, fmt.Errorf("invalid categorical probability")
					}
					sum += v.Score
				}
			}
			if o.Strategy != "lds" {
				// 2026-11-12: Choice의 반올림 오차만 보정하고 임의 점수는 분포로 바꾸지 않는다.
				if math.Abs(sum-1) > math.Max(0.0001, float64(len(ss))*0.000051) {
					return out, fmt.Errorf("invalid categorical probability sum")
				}
				for id, v := range scores {
					scores[id] = v / sum
				}
			}
			if chosen != "" {
				found := false
				for _, c := range candidates {
					if c.ID == chosen {
						found = true
					}
				}
				if !found {
					return out, fmt.Errorf("unknown chosen candidate")
				}
				if scores[chosen] < scores[ordered[0].ID] {
					return out, fmt.Errorf("chosen candidate below maximum")
				}
			}
			// 2026-11-12: 짧은 ID로 바꿔도 기존 부모별 동점 순위와 실제 최상위 선택을 유지한다.
			candidateIndex := map[string]int{}
			for i, c := range candidates {
				candidateIndex[c.ID] = i
			}
			sort.SliceStable(ordered, func(i, j int) bool {
				a, b := ordered[i], ordered[j]
				if scores[a.ID] != scores[b.ID] {
					return scores[a.ID] > scores[b.ID]
				}
				if chosen != "" && (a.ID == chosen || b.ID == chosen) {
					return a.ID == chosen
				}
				return strconv.Itoa(candidateIndex[a.ID]) < strconv.Itoa(candidateIndex[b.ID])
			})
			if !hit {
				decisions[key] = d
			}
		}
		byID := map[string]Choice{}
		for i, c := range candidates {
			byID[c.ID] = choices[i]
		}
		for i, c := range ordered {
			// 2026-11-12: 한 부모의 자식 수가 많아도 취소 뒤 추가 파서 확장을 시작하지 않는다.
			if err = ctx.Err(); err != nil {
				return
			}
			if o.Trace != nil && len(choices) > 1 {
				score := scores[c.ID]
				o.Trace(Event{ID: c.ID, ParentID: s.ID, Reason: "ranked", Lexeme: c.Lexeme, TokenKind: c.TokenKind, Rule: c.Rule, Rank: i + 1, Score: &score, ChosenID: chosen, Calls: out.Calls, Candidates: out.Candidates})
			}
			var next *State
			next, err = s.Advance(byID[c.ID], c.ID)
			if err != nil {
				return
			}
			next.rememberDeclaration(byID[c.ID].check)
			child := &searchNode{state: next, rankCost: n.rankCost + i, logProb: n.logProb, sequence: seq, parent: n, prior: 1}
			seq++
			if len(choices) == 1 {
				next.Forced++
				out.ForcedSteps++
			} else {
				next.Selected++
				out.ModelSteps++
				if o.Strategy != "lds" {
					child.prior = .99*scores[c.ID] + .01/float64(len(choices))
					child.logProb += math.Log(child.prior)
				}
			}
			out.GeneratedTokens++
			if o.Trace != nil {
				reason := "active"
				if len(choices) == 1 {
					reason = "forced"
				}
				event := Event{ID: next.ID, ParentID: s.ID, Reason: reason, Lexeme: c.Lexeme, TokenKind: c.TokenKind, Rule: c.Rule, Rank: i + 1, Calls: out.Calls, Candidates: out.Candidates}
				if o.TraceCode {
					event.Code = next.Source()
				}
				o.Trace(event)
			}
			switch o.Strategy {
			case "lds":
				child.priority = float64(child.rankCost)
			case "levin":
				child.priority = math.Log(float64(len(next.Tokens)+1)) - child.logProb
			case "phs":
				remaining := 0
				if o.RemainingTokens != nil {
					remaining = o.RemainingTokens(next)
					if remaining < 0 {
						remaining = 0
					}
				} else if next.Parser != nil {
					// 2026-11-12: 계산 불가 상태는 가지치지 않고 안전한 0 하한으로 취급한다.
					if min, ok := next.Parser.MinRemaining(); ok {
						remaining = min
					}
				}
				child.priority = math.Log(float64(len(next.Tokens)+remaining+1)) - child.logProb
			}
			if root != nil {
				n.children = append(n.children, child)
			} else {
				heap.Push(&q, child)
			}
		}
		// 2026-11-12: 한 simulation은 정책으로 토큰을 이어 완성 또는 실패할 때까지 유지한다.
		if root != nil && len(n.children) > 0 {
			rollout = samplePolicy(n.children, rng)
		}
	}
	if limited {
		return out, ErrBudget
	}
	return out, ErrNoSolution
}
