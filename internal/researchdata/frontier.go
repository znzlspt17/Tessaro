package researchdata

import (
	"fmt"
	"go/scanner"
	"go/token"
	"tessaro/internal/tokenspec"
	"tessaro/internal/tokensynth"
	"strings"
)

type Frontier struct {
	Reachable bool   `json:"reachable"`
	Blocked   string `json:"blocked_lexeme,omitempty"`
	Prefix    string `json:"prefix,omitempty"`
	Reason    string `json:"reason,omitempty"`
	Tokens    int    `json:"tokens"`
}

// 2026-11-12: 각 참조 토큰을 실제 선택지와 대조하되 차단은 해당 참조에만 한정한다.
func CheckFrontier(p tokenspec.Problem, source string) (Frontier, error) {
	var scan scanner.Scanner
	failures := 0
	scan.Init(token.NewFileSet().AddFile("reference.go", -1, len(source)), []byte(source), func(token.Position, string) { failures++ }, 0)
	var lex []string
	var kinds []token.Token
	for {
		_, k, lit := scan.Scan()
		if k == token.EOF {
			break
		}
		if lit == "" {
			lit = k.String()
		}
		if k == token.SEMICOLON {
			lit = ";"
		}
		lex = append(lex, lit)
		kinds = append(kinds, k)
	}
	if failures != 0 {
		return Frontier{}, fmt.Errorf("invalid reference tokens")
	}
	if len(lex) > 0 && lex[len(lex)-1] == ";" {
		lex = lex[:len(lex)-1]
		kinds = kinds[:len(kinds)-1]
	}
	result := Frontier{Tokens: len(lex)}
	state, err := tokensynth.NewState()
	if err != nil {
		return result, err
	}
	mapping := map[string]string{}
	used := map[string]bool{}
	for _, name := range p.Inputs {
		mapping[name] = name
		used[name] = true
	}
	for _, name := range []string{"main", "f0", "int", "bool", "string", "true", "false", "nil", "len", "cap", "append", "copy", "delete", "make", "_"} {
		mapping[name] = name
		used[name] = true
	}
	next := 0
	for i, value := range lex {
		if kinds[i] == token.IDENT {
			if mapped, ok := mapping[value]; ok {
				value = mapped
			} else {
				for {
					fresh := fmt.Sprintf("v%d", next)
					next++
					if !used[fresh] {
						mapping[value] = fresh
						used[fresh] = true
						value = fresh
						break
					}
				}
			}
		}
		if tokensynth.Complete(state) {
			result.Blocked = value
			result.Prefix = state.Source()
			result.Reason = "tokens_after_completion"
			return result, nil
		}
		choices, rejected, e := tokensynth.Candidates(p, state)
		if e != nil {
			return result, e
		}
		var selected *tokensynth.Choice
		for j := range choices {
			if choices[j].Lexeme == value {
				selected = &choices[j]
				break
			}
		}
		if selected == nil {
			result.Blocked = value
			result.Prefix = state.Source()
			result.Reason = "vocabulary_or_contract"
			for _, r := range rejected {
				if r.Choice.Lexeme == value {
					result.Reason = string(r.Reason)
					break
				}
			}
			if _, e := state.Parser.Advance(value); e != nil {
				result.Reason = "grammar"
			}
			return result, nil
		}
		state, err = state.Advance(*selected, "reference")
		if err != nil {
			return result, err
		}
	}
	result.Reachable = tokensynth.Complete(state)
	if !result.Reachable {
		result.Prefix = strings.Join(state.Tokens, " ")
		result.Reason = "incomplete"
	}
	return result, nil
}
