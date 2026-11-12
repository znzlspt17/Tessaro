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
	// 2026-11-12: 완료된 차트는 변경하지 않으므로 새 차트만 소유하고 과거 항목을 공유한다.
	next := &State{grammar: s.grammar, start: s.start, charts: append([]chart(nil), s.charts...)}
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

// 2026-11-08: 완결된 실제 식의 시작 위치를 복사해 반환하며 차트는 변경하지 않는다.
func (s *State) CompletedOrigins(position int, rule string) []int {
	if s == nil || s.grammar == nil || position < 0 || position >= len(s.charts) {
		return nil
	}
	seen := map[int]bool{}
	var result []int
	for _, it := range s.charts[position].items {
		if it.lhs == rule && it.dot == len(s.production(it).parts) && !seen[it.origin] {
			seen[it.origin] = true
			result = append(result, it.origin)
		}
	}
	sort.Ints(result)
	return result
}

// 2026-11-08: Earley 상위 경로를 과대 추정하여 모든 해석이 문장 왼쪽인 경우에만 확정한다.
// 알 수 없는 규칙, 조건식, 호출·인덱스 등은 유효한 다른 해석으로 남겨 둔다.
func (s *State) BinaryOnlyStatementLeft(position int, operator string) bool {
	return s.onlyStatementLeft(position, operator, "binary_expression")
}

// 2026-11-12: 논리 부정의 결과는 호출·대입·전송 대상이 될 수 없으므로 모든 상위 경로가
// 독립 문장 왼쪽일 때만 거부한다. 괄호나 다른 미확정 상위 해석은 기존처럼 보존한다.
func (s *State) NotOnlyStatementLeft(position int) bool {
	return s.onlyStatementLeft(position, "!", "unary_expression")
}

func (s *State) onlyStatementLeft(position int, operator, operatorRule string) bool {
	if s == nil || s.grammar == nil || position < 1 || position >= len(s.charts) {
		return false
	}
	type key struct {
		symbol, alias    string
		position, origin int
	}
	visiting := map[key]bool{}
	memo := map[key]bool{}
	done := map[key]bool{}
	statementFound := false
	var walk func(string, string, int, int) bool
	walk = func(symbol, alias string, position, origin int) bool {
		k := key{symbol, alias, position, origin}
		if visiting[k] {
			return true
		}
		if done[k] {
			return memo[k]
		}
		visiting[k] = true
		found, safe := false, true
		if position < 0 || position >= len(s.charts) {
			return false
		}
		for _, parent := range s.charts[position].items {
			p := s.production(parent)
			if parent.dot >= len(p.parts) {
				continue
			}
			expected := p.parts[parent.dot]
			if expected.terminal != nil || expected.symbol != symbol || s.aliasFor(parent, symbol) != alias {
				continue
			}
			found = true
			if origin < 0 {
				if parent.lhs == operatorRule {
					if !walk(parent.lhs, parent.alias, parent.origin, parent.origin) {
						safe = false
					}
				} else if len(parent.lhs) > 0 && parent.lhs[0] == 0 {
					if !walk(parent.lhs, parent.alias, parent.origin, origin) {
						safe = false
					}
				} else {
					safe = false
				}
				continue
			}
			switch parent.lhs {
			case "expression_statement", "assignment_statement", "inc_statement", "dec_statement", "send_statement", "short_var_declaration":
				if parent.origin != origin {
					safe = false
				} else {
					statementFound = true
				}
			case "_expression", "expression_list", "binary_expression":
				if parent.origin != origin || !walk(parent.lhs, parent.alias, parent.origin, origin) {
					safe = false
				}
			case "selector_expression", "slice_expression", "index_expression", "type_assertion_expression", "call_expression":
				// 괄호 없는 후위 연산자는 이항식이 아니라 오른쪽 피연산자에 결합된다.
				if parent.origin != origin {
					safe = false
				}
			default:
				if len(parent.lhs) > 0 && parent.lhs[0] == 0 {
					if !walk(parent.lhs, parent.alias, parent.origin, origin) {
						safe = false
					}
				} else {
					safe = false
				}
			}
		}
		delete(visiting, k)
		done[k] = true
		memo[k] = found && safe
		return memo[k]
	}
	found := false
	for _, it := range s.charts[position].items {
		if it.dot == 0 {
			continue
		}
		part := s.production(it).parts[it.dot-1]
		if part.terminal == nil || part.terminal.Rule != operatorRule || part.terminal.Value != operator {
			continue
		}
		found = true
		if !walk(it.lhs, it.alias, it.origin, -1) {
			return false
		}
	}
	return found && statementFound
}

// 2026-11-12: 과거 차트는 불변으로 공유하고 현재 차트는 독립적으로 복사한다.
func (s *State) Clone() *State {
	if s == nil {
		return nil
	}
	out := &State{grammar: s.grammar, start: s.start, charts: append([]chart(nil), s.charts...)}
	if len(out.charts) == 0 {
		return out
	}
	i := len(out.charts) - 1
	c := s.charts[i]
	out.charts[i].items = append([]item(nil), c.items...)
	out.charts[i].seen = make(map[item]bool, len(c.seen))
	for k, v := range c.seen {
		out.charts[i].seen[k] = v
	}
	return out
}
