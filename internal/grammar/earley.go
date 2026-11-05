package grammar

import (
	"fmt"
	"sort"
)

type item struct {
	lhs               string
	alias             string
	prod, dot, origin int
}

func (s *State) aliasFor(parent item, symbol string) string {
	if alias := s.grammar.aliases[symbol]; alias != "" {
		return alias
	}
	return parent.alias
}

type chart struct {
	items []item
	seen  map[item]bool
}

func (c *chart) add(i item) bool {
	if c.seen == nil {
		c.seen = map[item]bool{}
	}
	if c.seen[i] {
		return false
	}
	c.seen[i] = true
	c.items = append(c.items, i)
	return true
}

type State struct {
	grammar *Grammar
	start   string
	charts  []chart
}

const startSymbol = "\x00start"

func (g *Grammar) NewState(startRule string) *State {
	s := &State{grammar: g, start: startRule, charts: []chart{{}}}
	if g == nil {
		return s
	}
	if _, ok := g.rules[startRule]; !ok {
		return s
	}
	s.charts[0].add(item{lhs: startSymbol})
	s.close()
	return s
}
func (s *State) production(i item) production {
	if i.lhs == startSymbol {
		return production{parts: []part{{symbol: s.start}}}
	}
	return s.grammar.rules[i.lhs][i.prod]
}

// 2026-11-05: 동일 접두사의 모든 분석 경로를 유지하며 nullable과 좌재귀를 고정점까지 확장한다.
func (s *State) close() {
	idx := len(s.charts) - 1
	current := &s.charts[idx]
	for {
		before := len(current.items)
		for i := 0; i < len(current.items); i++ {
			it := current.items[i]
			p := s.production(it)
			if it.dot < len(p.parts) {
				next := p.parts[it.dot]
				if next.terminal == nil {
					for j := range s.grammar.rules[next.symbol] {
						current.add(item{lhs: next.symbol, prod: j, origin: idx, alias: s.aliasFor(it, next.symbol)})
					}
				}
				continue
			}
			origin := &s.charts[it.origin]
			for j := 0; j < len(origin.items); j++ {
				parent := origin.items[j]
				pp := s.production(parent)
				if parent.dot < len(pp.parts) {
					expected := pp.parts[parent.dot]
					if expected.terminal == nil && expected.symbol == it.lhs && s.aliasFor(parent, expected.symbol) == it.alias {
						parent.dot++
						current.add(parent)
					}
				}
			}
		}
		if len(current.items) == before {
			return
		}
	}
}
func (s *State) Next() []Terminal {
	if s == nil || s.grammar == nil || len(s.charts) == 0 {
		return nil
	}
	seen := map[Terminal]bool{}
	var result []Terminal
	for _, it := range s.charts[len(s.charts)-1].items {
		p := s.production(it)
		if it.dot < len(p.parts) && p.parts[it.dot].terminal != nil {
			t := *p.parts[it.dot].terminal
			if t.Kind == "lexical" && t.Rule == "identifier" && it.alias != "" {
				t.Rule = it.alias
			}
			if !seen[t] {
				seen[t] = true
				result = append(result, t)
			}
		}
	}
	sort.Slice(result, func(i, j int) bool {
		a, b := result[i], result[j]
		if a.Kind != b.Kind {
			return a.Kind < b.Kind
		}
		if a.Value != b.Value {
			return a.Value < b.Value
		}
		return a.Rule < b.Rule
	})
	return result
}
func (s *State) Advance(lexeme string) (*State, error) {
	if s == nil || s.grammar == nil || len(s.charts) == 0 {
		return nil, fmt.Errorf("invalid parser state")
	}
	next := s.Clone()
	old := &s.charts[len(s.charts)-1]
	c := chart{}
	for _, it := range old.items {
		p := s.production(it)
		if it.dot < len(p.parts) && p.parts[it.dot].terminal != nil && matches(*p.parts[it.dot].terminal, lexeme) {
			it.dot++
			c.add(it)
		}
	}
	if len(c.items) == 0 {
		return nil, fmt.Errorf("token is not accepted")
	}
	next.charts = append(next.charts, c)
	next.close()
	return next, nil
}
func (s *State) Accepts() bool {
	if s == nil || len(s.charts) == 0 {
		return false
	}
	return s.charts[len(s.charts)-1].seen[item{lhs: startSymbol, dot: 1}]
}
func (s *State) Clone() *State {
	if s == nil {
		return nil
	}
	out := &State{grammar: s.grammar, start: s.start, charts: make([]chart, len(s.charts))}
	for i, c := range s.charts {
		out.charts[i].items = append([]item(nil), c.items...)
		out.charts[i].seen = make(map[item]bool, len(c.seen))
		for k, v := range c.seen {
			out.charts[i].seen[k] = v
		}
	}
	return out
}
