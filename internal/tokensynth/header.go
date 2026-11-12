package tokensynth

import (
	"fmt"
	"go/token"
	"tessaro/internal/tokenspec"
	"tessaro/internal/tokenverify"
)

type headerPhase uint8

const (
	hBeforeFunc headerPhase = iota
	hFunctionName
	hOpenParams
	hParamFirst
	hParamAfterName
	hParamGroupName
	hParamAfterType
	hParamAfterComma
	hType
	hReturnFirst
	hResultFirst
	hResultNamedType
	hResultAfterType
	hResultAfterComma
	hReturnDone
	hBody
)

type headerBranch struct {
	phase         headerPhase
	used, pending uint16
	groupType     string
	target        []string
	ends          map[int]int
	pos           int
	wrap          []int
	after         headerPhase
}
type headerState struct {
	names    []string
	types    map[string]string
	result   string
	branches []headerBranch
}

func newHeader(p tokenspec.Problem) (*headerState, error) {
	if err := p.Validate(); err != nil {
		return nil, fmt.Errorf("invalid problem contract")
	}
	contract, result, err := p.Contract()
	if err != nil {
		return nil, fmt.Errorf("invalid problem contract")
	}
	return &headerState{names: append([]string(nil), p.Inputs...), types: contract, result: result, branches: []headerBranch{{phase: hBeforeFunc}}}, nil
}
func (h *headerState) all() uint16 { return uint16((1 << len(h.names)) - 1) }
func (h *headerState) nameIndex(name string) int {
	for i, n := range h.names {
		if n == name {
			return i
		}
	}
	return -1
}
func (h *headerState) freshName(name string) bool {
	return name != "_" && token.IsIdentifier(name) && h.nameIndex(name) < 0
}
func typeParts(typ string) ([]string, map[int]int, error) {
	switch typ {
	case "int", "bool", "string":
		return []string{typ}, map[int]int{0: 1}, nil
	case "[]int":
		return []string{"[", "]", "int"}, map[int]int{0: 3, 2: 3}, nil
	case "[]string":
		return []string{"[", "]", "string"}, map[int]int{0: 3, 2: 3}, nil
	case "map[string]int":
		return []string{"map", "[", "string", "]", "int"}, map[int]int{0: 5, 2: 3, 4: 5}, nil
	case "map[string]string":
		return []string{"map", "[", "string", "]", "string"}, map[int]int{0: 5, 2: 3, 4: 5}, nil
	}
	return nil, nil, fmt.Errorf("unsupported contract type")
}
func beginType(b headerBranch, typ string, after headerPhase) (headerBranch, error) {
	parts, ends, err := typeParts(typ)
	if err != nil {
		return b, err
	}
	b.phase = hType
	b.target = parts
	b.ends = ends
	b.pos = 0
	b.wrap = nil
	b.after = after
	return b, nil
}
func (h *headerState) step(b headerBranch, value string) ([]headerBranch, error) {
	if b.phase == hType && b.pos == len(b.target) && len(b.wrap) == 0 {
		b.phase = b.after
		if b.phase == hParamAfterType {
			b.used |= b.pending
			b.pending = 0
		}
		return h.step(b, value)
	}
	switch b.phase {
	case hBeforeFunc:
		if value == "func" {
			b.phase = hFunctionName
		}
		return []headerBranch{b}, nil
	case hFunctionName:
		b.phase = hOpenParams
		return []headerBranch{b}, nil
	case hOpenParams:
		if value == "(" {
			b.phase = hParamFirst
			return []headerBranch{b}, nil
		}
	case hParamFirst, hParamAfterComma:
		if value == ")" && b.used == h.all() {
			b.phase = hReturnFirst
			return []headerBranch{b}, nil
		}
		i := h.nameIndex(value)
		if i >= 0 && (b.used&(1<<i)) == 0 {
			b.pending = 1 << i
			b.groupType = h.types[value]
			b.phase = hParamAfterName
			return []headerBranch{b}, nil
		}
	case hParamGroupName:
		i := h.nameIndex(value)
		if i >= 0 && (b.used&(1<<i)) == 0 && (b.pending&(1<<i)) == 0 && h.types[value] == b.groupType {
			b.pending |= 1 << i
			b.phase = hParamAfterName
			return []headerBranch{b}, nil
		}
	case hParamAfterName:
		if value == "," {
			for i, n := range h.names {
				bit := uint16(1 << i)
				if (b.used|b.pending)&bit == 0 && h.types[n] == b.groupType {
					b.phase = hParamGroupName
					return []headerBranch{b}, nil
				}
			}
			return nil, nil
		}
		next, err := beginType(b, b.groupType, hParamAfterType)
		if err != nil {
			return nil, err
		}
		return h.step(next, value)
	case hParamAfterType:
		if value == "," {
			b.phase = hParamAfterComma
			return []headerBranch{b}, nil
		}
		if value == ")" && b.used == h.all() {
			b.phase = hReturnFirst
			return []headerBranch{b}, nil
		}
	case hType:
		if len(b.wrap) > 0 && b.pos == b.wrap[len(b.wrap)-1] {
			if value == ")" {
				b.wrap = append([]int(nil), b.wrap[:len(b.wrap)-1]...)
				return []headerBranch{b}, nil
			}
			return nil, nil
		}
		if end, ok := b.ends[b.pos]; ok && value == "(" {
			b.wrap = append(append([]int(nil), b.wrap...), end)
			return []headerBranch{b}, nil
		}
		if b.pos < len(b.target) && value == b.target[b.pos] {
			b.pos++
			return []headerBranch{b}, nil
		}
	case hReturnFirst:
		if value == "(" {
			list := b
			list.phase = hResultFirst
			direct, err := beginType(b, h.result, hReturnDone)
			if err != nil {
				return nil, err
			}
			d, err := h.step(direct, value)
			return append(d, list), err
		}
		next, err := beginType(b, h.result, hReturnDone)
		if err != nil {
			return nil, err
		}
		return h.step(next, value)
	case hResultFirst:
		var result []headerBranch
		if h.freshName(value) {
			named := b
			named.phase = hResultNamedType
			result = append(result, named)
		}
		next, err := beginType(b, h.result, hResultAfterType)
		if err != nil {
			return nil, err
		}
		typed, err := h.step(next, value)
		return append(result, typed...), err
	case hResultNamedType:
		next, err := beginType(b, h.result, hResultAfterType)
		if err != nil {
			return nil, err
		}
		return h.step(next, value)
	case hResultAfterType:
		if value == ")" {
			b.phase = hReturnDone
			return []headerBranch{b}, nil
		}
		if value == "," {
			b.phase = hResultAfterComma
			return []headerBranch{b}, nil
		}
	case hResultAfterComma:
		if value == ")" {
			b.phase = hReturnDone
			return []headerBranch{b}, nil
		}
	case hReturnDone:
		if value == "{" {
			b.phase = hBody
			return []headerBranch{b}, nil
		}
	case hBody:
		return []headerBranch{b}, nil
	}
	return nil, nil
}

// 2026-11-05: 입력 계약으로 유효한 선언 접두사만 유지하고 가능한 해석을 함께 보존한다.
func (h *headerState) advance(value string) (*headerState, tokenverify.DeclarationReason, error) {
	out := &headerState{names: h.names, types: h.types, result: h.result}
	reason := tokenverify.DeclarationReason("invalid_signature")
	for _, b := range h.branches {
		if value == "..." && b.phase != hBody {
			reason = "variadic"
		} else if b.phase == hType && b.after != hParamAfterType {
			reason = "return_type"
		} else if b.phase >= hReturnFirst && b.phase < hBody {
			reason = "return_type"
		} else if b.phase == hParamFirst || b.phase == hParamAfterComma {
			if b.used == h.all() {
				reason = "parameter_count"
			} else {
				reason = "parameter_name"
			}
		} else if b.phase == hParamGroupName {
			if h.nameIndex(value) >= 0 {
				reason = "parameter_type"
			} else {
				reason = "parameter_name"
			}
		} else if b.phase == hParamAfterType {
			reason = "parameter_count"
		} else if b.phase == hParamAfterName || b.phase == hType && b.after == hParamAfterType {
			reason = "parameter_type"
		}
		next, err := h.step(b, value)
		if err != nil {
			return nil, "", err
		}
		out.branches = append(out.branches, next...)
	}
	if len(out.branches) == 0 {
		return nil, reason, nil
	}
	return out, "", nil
}
func semanticFor(p tokenspec.Problem, s *State) (*headerState, tokenverify.DeclarationReason, error) {
	if s.header != nil {
		contract, result, err := p.Contract()
		if err != nil || result != s.header.result || len(p.Inputs) != len(s.header.names) {
			return nil, "", fmt.Errorf("problem contract changed")
		}
		for i, name := range p.Inputs {
			if s.header.names[i] != name || contract[name] != s.header.types[name] {
				return nil, "", fmt.Errorf("problem contract changed")
			}
		}
		return s.header, "", nil
	}
	h, err := newHeader(p)
	if err != nil {
		return nil, "", err
	}
	for _, value := range s.Tokens {
		var reason tokenverify.DeclarationReason
		h, reason, err = h.advance(value)
		if err != nil || reason != "" {
			return nil, reason, err
		}
	}
	return h, "", nil
}
