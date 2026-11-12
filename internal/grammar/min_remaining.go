package grammar

const infiniteYield = int(^uint(0)>>1) / 4

// 2026-11-12: 문법별 최소 생성 길이는 불변이므로 공유 Grammar에서 한 번만 안전하게 계산한다.
func (g *Grammar) minimumYields() map[string]int {
	g.minOnce.Do(func() {
		yields := make(map[string]int, len(g.rules))
		for name := range g.rules {
			yields[name] = infiniteYield
		}
		changed := true
		for changed {
			changed = false
			for name, productions := range g.rules {
				for _, p := range productions {
					n := yieldParts(p.parts, yields)
					if n < yields[name] {
						yields[name] = n
						changed = true
					}
				}
			}
		}
		g.minYields = yields
	})
	return g.minYields
}

func yieldParts(parts []part, yields map[string]int) int {
	n := 0
	for _, p := range parts {
		v := 1
		if p.terminal == nil {
			var ok bool
			v, ok = yields[p.symbol]
			if !ok {
				return infiniteYield
			}
		}
		if v >= infiniteYield-n {
			return infiniteYield
		}
		n += v
	}
	return n
}

type continuationKey struct {
	origin        int
	symbol, alias string
}
type chartItemRef struct{ chart, index int }

// 2026-11-12: 각 항목의 나머지뿐 아니라 원점 차트의 모든 부모 경로를 고정점까지 따라가야 전체 시작 규칙의 하한을 과대 추정하지 않는다.
// 계산할 수 없는 경로는 false로 남겨 호출자가 가지치기하지 않도록 한다.
func (s *State) MinRemaining() (int, bool) {
	if s == nil || s.grammar == nil || len(s.charts) == 0 {
		return 0, false
	}
	if s.Accepts() {
		return 0, true
	}
	yields := s.grammar.minimumYields()
	children := make(map[continuationKey][]chartItemRef)
	distances := make([][]int, len(s.charts))
	queue := make([]chartItemRef, 0)
	for pos, c := range s.charts {
		distances[pos] = make([]int, len(c.items))
		for i, it := range c.items {
			distances[pos][i] = infiniteYield
			ref := chartItemRef{pos, i}
			if it.lhs == startSymbol {
				distances[pos][i] = 0
				queue = append(queue, ref)
				continue
			}
			k := continuationKey{it.origin, it.lhs, it.alias}
			children[k] = append(children[k], ref)
		}
	}
	for head := 0; head < len(queue); head++ {
		ref := queue[head]
		parent := s.charts[ref.chart].items[ref.index]
		p := s.production(parent)
		if parent.dot >= len(p.parts) {
			continue
		}
		expected := p.parts[parent.dot]
		if expected.terminal != nil {
			continue
		}
		cost := yieldParts(p.parts[parent.dot+1:], yields)
		base := distances[ref.chart][ref.index]
		if cost >= infiniteYield-base {
			continue
		}
		cost += base
		k := continuationKey{ref.chart, expected.symbol, s.aliasFor(parent, expected.symbol)}
		for _, child := range children[k] {
			if cost < distances[child.chart][child.index] {
				distances[child.chart][child.index] = cost
				queue = append(queue, child)
			}
		}
	}
	best := infiniteYield
	last := len(s.charts) - 1
	for i, it := range s.charts[last].items {
		p := s.production(it)
		suffix := yieldParts(p.parts[it.dot:], yields)
		cont := distances[last][i]
		if suffix < infiniteYield-cont && suffix+cont < best {
			best = suffix + cont
		}
	}
	if best == infiniteYield {
		return 0, false
	}
	return best, true
}
